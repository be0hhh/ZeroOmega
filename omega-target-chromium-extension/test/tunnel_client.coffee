assert = require 'assert'
TunnelClient = require '../src/module/tunnel_client'

event = ->
  listeners = []
  addListener: (fn) -> listeners.push(fn)
  emit: (value) -> fn(value) for fn in listeners

fixture = (timeout = 500) ->
  port =
    onMessage: event()
    onDisconnect: event()
    messages: []
  port.postMessage = (message) -> port.messages.push(message)
  runtime =
    lastError: null
    connectNative: (name) ->
      assert.equal(name, 'io.github.zeroomega.wg')
      port
  {client: new TunnelClient(runtime, Promise, timeout), port, runtime}

describe 'Native tunnel bridge', ->
  it 'matches out-of-order responses to requests on one native port', ->
    {client, port} = fixture()
    first = client.request('status', configId: 'first')
    second = client.request('status', configId: 'second')
    port.onMessage.emit({
      id: port.messages[1].id, ok: true, result: {configId: 'second'}
    })
    port.onMessage.emit({
      id: port.messages[0].id, ok: true, result: {configId: 'first'}
    })
    Promise.all([first, second]).then (results) ->
      assert.equal(results[0].configId, 'first')
      assert.equal(results[1].configId, 'second')

  it 'rejects every pending request when the helper disappears', ->
    {client, port, runtime} = fixture()
    results = []
    for configId in ['first', 'second']
      results.push(client.request('connect', {configId}).then(->
        throw new Error('Expected unavailable helper')
      , (error) ->
        assert.equal(error.code, 'NATIVE_DISCONNECTED')
        assert.equal(error.message.indexOf('private-key'), -1)
      ))
    runtime.lastError = {message: 'private-key must not be exposed'}
    port.onDisconnect.emit()
    Promise.all(results)

  it 'times out without logging or echoing the imported configuration', ->
    {client} = fixture(5)
    client.request('import', config: 'private-key').then(->
      throw new Error('Expected timeout')
    , (error) ->
      assert.equal(error.code, 'NATIVE_TIMEOUT')
      assert.equal(error.message.indexOf('private-key'), -1)
    )

  it 'only sends the command-specific fields, never unrelated state', ->
    {client, port} = fixture()
    pending = client.request('status', {
      configId: 'test', config: 'secret', password: 'secret', host: 'remote'
    })
    assert.deepEqual(port.messages[0],
      {id: '1', command: 'status', configId: 'test'})
    port.onMessage.emit({id: '1', ok: true, result: {state: 'disconnected'}})
    pending

  it 'handles missing helpers and malformed responses with clear errors', ->
    {client, port, runtime} = fixture()
    runtime.connectNative = -> throw new Error('sensitive local path')
    client.request('version').then(->
      throw new Error('Expected missing host')
    , (error) ->
      assert.equal(error.code, 'NATIVE_UNAVAILABLE')
      assert.equal(error.message.indexOf('sensitive'), -1)
    ).then ->
      {client, port} = fixture()
      pending = client.request('version')
      port.onMessage.emit({id: '1', ok: true, result: null})
      pending.then(->
        throw new Error('Expected invalid response')
      , (error) -> assert.equal(error.code, 'INVALID_RESPONSE'))
