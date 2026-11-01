assert = require('assert')
fs = require('fs')
path = require('path')
vm = require('vm')
coffee = require('coffee-script')

source = fs.readFileSync(path.join(__dirname,
  '../src/omega/controllers/tunnel_profile.coffee'), 'utf8')
controller = null
vm.runInNewContext coffee.compile(source),
  Blob: Blob
  angular:
    module: ->
      controller: (name, implementation) -> controller = implementation

tick = -> new Promise((resolve) -> setImmediate(resolve))

createEditor = (handler, tunnel) ->
  callbacks = {}
  requests = []
  scope =
    profile: {name: 'test', profileType: 'TunnelProfile'}
    options: {}
    $on: (event, callback) -> callbacks[event] = callback
  scope.profile.tunnel = tunnel if tunnel
  scope.options['+test'] = scope.profile
  interval = -> {poll: true}
  interval.cancel = (timer) -> interval.cancelled = timer
  window = {confirm: -> true}
  q = {when: (value) -> Promise.resolve(value)}
  target =
    tunnelRequest: (command, fields) ->
      requests.push({command, fields})
      Promise.resolve().then(-> handler(command, fields))
  controller(scope, interval, window, q, target, (key) -> key)
  {scope, requests, callbacks, interval, window, ready: tick()}

describe 'Tunnel profile editor', ->
  it 'keeps imported private text out of profile metadata and backups', (done) ->
    editor = createEditor (command) ->
      if command == 'version'
        {protocolVersion: 1}
      else
        configId: 'abc'
        protocol: 'amneziawg'
        port: 21000
        state: 'disconnected'
        config: 'untrusted secret response'
    editor.ready.then(->
      editor.scope.tunnelEditor.config = '[Interface]\nPrivateKey=secret'
      editor.scope.tunnelImport()
    ).then(-> tick()).then(->
      stored = JSON.stringify(editor.scope.options)
      assert.strictEqual(stored.indexOf('secret'), -1)
      assert.strictEqual(stored.indexOf('PrivateKey'), -1)
      assert.strictEqual(editor.scope.profile.tunnel.configId, 'abc')
      assert.strictEqual(editor.scope.tunnelEditor.config, '')
      assert.strictEqual(editor.requests[1].fields.config,
        '[Interface]\nPrivateKey=secret')
      done()
    ).catch(done)

  it 'limits UTF-8 bytes before sending config to the native host', (done) ->
    editor = createEditor -> {protocolVersion: 1}
    editor.ready.then(->
      editor.scope.tunnelEditor.config = new Array(140001).join('я')
      editor.scope.tunnelImport()
      assert.strictEqual(editor.requests.length, 1)
      assert.strictEqual(editor.scope.tunnelEditor.error,
        'options_tunnel_configTooLarge')
      done()
    ).catch(done)

  it 'recovers empty-profile status after the helper becomes available', (done) ->
    available = false
    editor = createEditor ->
      if available
        {protocolVersion: 1}
      else
        throw {code: 'NATIVE_UNAVAILABLE', message: 'not installed'}
    editor.ready.then(->
      assert.strictEqual(editor.scope.tunnelEditor.helperUnavailable, true)
      assert.strictEqual(editor.scope.tunnelEditor.state, 'error')
      available = true
      editor.scope.tunnelRefresh()
    ).then(-> tick()).then(->
      assert.strictEqual(editor.scope.tunnelEditor.helperUnavailable, false)
      assert.strictEqual(editor.scope.tunnelEditor.state, 'disconnected')
      done()
    ).catch(done)

  it 'does not delete a configuration shared with another profile', (done) ->
    tunnel = {configId: 'abc', protocol: 'wireguard', port: 21000}
    editor = createEditor((-> {state: 'disconnected'}), tunnel)
    editor.ready.then(->
      editor.scope.options['+other'] = {tunnel: tunnel}
      editor.scope.tunnelRemove()
      assert.strictEqual(editor.requests.length, 1)
      assert.strictEqual(editor.scope.tunnelEditor.error,
        'options_tunnel_configReferenced')
      assert.ok(editor.scope.profile.tunnel)
      done()
    ).catch(done)

  it 'repairs a restored profile with a new local config after not-found', (done) ->
    tunnel = {configId: 'other-computer', protocol: 'wireguard', port: 21000}
    editor = createEditor(((command, fields) ->
      if command == 'status'
        throw {code: 'config_not_found'}
      else
        assert.strictEqual(fields.configId, undefined)
        {configId: 'local-config', protocol: 'wireguard', port: 22000}
    ), tunnel)
    editor.ready.then(->
      assert.strictEqual(editor.scope.tunnelEditor.configMissing, true)
      editor.scope.tunnelEditor.config = 'private restored configuration'
      editor.scope.tunnelImport()
    ).then(-> tick()).then(->
      assert.strictEqual(editor.scope.profile.name, 'test')
      assert.strictEqual(editor.scope.profile.tunnel.configId, 'local-config')
      assert.strictEqual(editor.scope.tunnelEditor.configMissing, false)
      assert.strictEqual(editor.requests.length, 2)
      done()
    ).catch(done)

  it 'keeps the existing id after ambiguous timeouts and never retries imports', (done) ->
    tunnel = {configId: 'existing', protocol: 'wireguard', port: 21000}
    editor = createEditor(((command) ->
      if command == 'status'
        {state: 'disconnected'}
      else
        throw {code: 'NATIVE_TIMEOUT'}
    ), tunnel)
    editor.ready.then(->
      editor.scope.tunnelEditor.config = 'replacement configuration'
      editor.scope.tunnelImport()
    ).then(-> tick()).then(->
      assert.strictEqual(editor.requests.length, 2)
      assert.strictEqual(editor.requests[1].fields.configId, 'existing')
      assert.strictEqual(editor.scope.profile.tunnel.configId, 'existing')
      assert.strictEqual(editor.scope.tunnelEditor.configMissing, false)
      done()
    ).catch(done)

  it 'forgets only stale metadata after explicit removal of a missing config', (done) ->
    tunnel = {configId: 'missing', protocol: 'wireguard', port: 21000}
    editor = createEditor((->
      throw {code: 'config_not_found'}
    ), tunnel)
    editor.ready.then(->
      editor.scope.options['+other'] = {tunnel: tunnel}
      editor.scope.tunnelRemove()
      assert.strictEqual(editor.requests.length, 1)
      assert.strictEqual(editor.scope.profile.tunnel, undefined)
      assert.strictEqual(editor.scope.options['+other'].tunnel.configId, 'missing')
      assert.strictEqual(editor.scope.tunnelEditor.configMissing, false)
      done()
    ).catch(done)

  it 'clears metadata if the native config disappears just before deletion', (done) ->
    tunnel = {configId: 'disappearing', protocol: 'wireguard', port: 21000}
    editor = createEditor(((command) ->
      if command == 'status'
        {state: 'disconnected'}
      else
        throw {code: 'config_not_found'}
    ), tunnel)
    editor.ready.then(-> editor.scope.tunnelRemove()).then(-> tick()).then(->
      assert.strictEqual(editor.requests[1].command, 'delete')
      assert.strictEqual(editor.scope.profile.tunnel, undefined)
      assert.strictEqual(editor.scope.tunnelEditor.error, '')
      done()
    ).catch(done)

  it 'clears secrets on editor destruction and ignores pending results', (done) ->
    completeImport = null
    editor = createEditor (command) ->
      if command == 'version'
        {protocolVersion: 1}
      else
        new Promise((resolve) -> completeImport = resolve)
    editor.ready.then(->
      editor.scope.tunnelEditor.config = 'private config'
      editor.scope.tunnelImport()
      null
    ).then(-> tick()).then(->
      editor.callbacks['$destroy']()
      assert.strictEqual(editor.scope.tunnelEditor.config, '')
      assert.ok(editor.interval.cancelled)
      completeImport({configId: 'abc', protocol: 'wireguard', port: 21000})
    ).then(-> tick()).then(->
      assert.strictEqual(editor.scope.profile.tunnel, undefined)
      done()
    ).catch(done)
