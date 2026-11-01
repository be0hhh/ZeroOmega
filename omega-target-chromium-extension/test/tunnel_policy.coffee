assert = require 'assert'
# Exercise source modules, not the browser-only generated bundle (whose
# intentional _process external cannot be loaded directly in Node).
OmegaTarget = require '../../omega-target/index.coffee'
targetPath = require.resolve('omega-target')
oldTargetModule = require.cache[targetPath]
require.cache[targetPath] = {exports: OmegaTarget, loaded: true}
Profiles = OmegaTarget.OmegaPac.Profiles
FirefoxImpl = require '../src/module/proxy/proxy_impl_firefox'
ListenerImpl = require '../src/module/proxy/proxy_impl_listener'
SettingsImpl = require '../src/module/proxy/proxy_impl_settings'
oldGlobalTarget = global.OmegaTarget
global.OmegaTarget = OmegaTarget
ChromeOptions = require '../src/module/options'
global.OmegaTarget = oldGlobalTarget
if oldTargetModule
  require.cache[targetPath] = oldTargetModule
else
  delete require.cache[targetPath]

describe 'Verified tunnel policy', ->
  oldChrome = null
  oldOmegaPac = null
  tunnel = null
  options = null
  beforeEach ->
    oldChrome = global.chrome
    oldOmegaPac = global.OmegaPac
    global.OmegaPac = OmegaTarget.OmegaPac
    Profiles.clearTunnelVerifications()
    tunnel = {
      name: 'vpn', profileType: 'TunnelProfile', revision: 'one'
      proxyDNS: false
      tunnel: {configId: 'config-a', protocol: 'wireguard', port: 24000}
    }
    options = {'+vpn': tunnel}
  afterEach ->
    Profiles.clearTunnelVerifications()
    global.chrome = oldChrome
    global.OmegaPac = oldOmegaPac

  authorize = ->
    Profiles.setTunnelVerification('config-a', {
      configId: 'config-a', protocol: 'wireguard', port: 24000
      state: 'connecting'
    })

  for Impl in [FirefoxImpl, ListenerImpl]
    do (Impl) ->
      it "#{Impl.name} blocks unverified/lost profiles, DNS stays remote", ->
        impl = Object.create(Impl.prototype)
        impl._optionsReady = Promise.resolve()
        impl._options = options
        impl._profile = tunnel
        impl.onRequest({url: 'https://example.com/'}).then (proxy) ->
          assert.equal(proxy[0].port, 9)
          assert.equal(proxy[0].type, 'socks')
          authorize()
          impl.onRequest({url: 'https://example.com/'})
        .then (proxy) ->
          assert.equal(proxy[0].port, 24000)
          assert.equal(proxy[0].proxyDNS, true)
          Profiles.clearTunnelVerifications()
          impl.onRequest({url: 'https://example.com/'})
        .then (proxy) ->
          assert.equal(proxy[0].port, 9)
          assert.equal(proxy.length, 1)

  it 'Chromium replaces cached authorization on loss with mandatory PAC', ->
    config = null
    global.chrome = {
      runtime: {lastError: null}
      proxy: {settings: {
        set: (args, cb) ->
          config = args.value
          cb()
        get: (args, cb) -> cb({})
      }}
    }
    impl = Object.create(SettingsImpl.prototype)
    impl.setProxyAuth = -> OmegaTarget.Promise.resolve()
    route = ->
      assert.equal(config.pacScript.mandatory, true)
      pac = config.pacScript.data
      func = eval("(function () { #{pac}; return FindProxyForURL; })()")
      func('https://example.com/', 'example.com')
    impl.applyProfile(tunnel, tunnel, options).then ->
      assert.equal(route(), 'SOCKS5 127.0.0.1:9')
      authorize()
      impl.applyProfile(tunnel, tunnel, options)
    .then ->
      assert.equal(route(), 'SOCKS5 127.0.0.1:24000')
      Profiles.clearTunnelVerifications()
      impl.applyProfile(tunnel, tunnel, options)
    .then ->
      assert.equal(route(), 'SOCKS5 127.0.0.1:9')

  it 'revokes the native mapping before issuing a disconnect request', ->
    authorize()
    applied = []
    controller = Object.create(ChromeOptions.prototype)
    controller.currentProfile = -> tunnel
    controller._options = options
    controller.proxyImpl = {applyProfile: ->
      applied.push(Profiles.tunnelProxy(tunnel).port)
      OmegaTarget.Promise.resolve()
    }
    controller._tunnelClient = {request: (command) ->
      assert.equal(command, 'disconnect')
      assert.deepEqual(applied, [9])
      OmegaTarget.Promise.resolve({configId: 'config-a', state: 'disconnected'})
    }
    controller.tunnelRequest('disconnect', configId: 'config-a').then ->
      assert.equal(Profiles.tunnelProxy(tunnel).port, 9)

  it 'ignores a stale connect response arriving after explicit disconnect', ->
    controller = Object.create(ChromeOptions.prototype)
    controller.currentProfile = -> tunnel
    controller._options = options
    controller.proxyImpl = {applyProfile: -> OmegaTarget.Promise.resolve()}
    resolveConnect = null
    controller._tunnelClient = {request: (command) ->
      if command == 'connect'
        new OmegaTarget.Promise (resolve) -> resolveConnect = resolve
      else
        OmegaTarget.Promise.resolve({
          configId: 'config-a', state: 'disconnected'
        })
    }
    connecting = controller.tunnelRequest('connect', configId: 'config-a')
    OmegaTarget.Promise.delay(1).then ->
      controller.tunnelRequest('disconnect', configId: 'config-a')
    .then ->
      resolveConnect({
        configId: 'config-a', protocol: 'wireguard', port: 24000
        state: 'connecting'
      })
      connecting
    .then ->
      assert.equal(Profiles.tunnelProxy(tunnel).port, 9)

  it 'ignores connect responses when the native port has already died', ->
    port = {
      onMessage: {addListener: (fn) -> port.receive = fn}
      onDisconnect: {addListener: (fn) -> port.disconnected = fn}
      postMessage: (request) ->
        port.receive({id: request.id, ok: true, result: {
          configId: 'config-a', protocol: 'wireguard', port: 24000
          state: 'connecting'
        }})
        port.disconnected()
    }
    global.chrome = {runtime: {connectNative: -> port}}
    controller = Object.create(ChromeOptions.prototype)
    controller.currentProfile = -> tunnel
    controller._options = options
    controller.log = {error: -> null}
    controller.proxyImpl = {applyProfile: -> OmegaTarget.Promise.resolve()}
    controller.tunnelRequest('connect', configId: 'config-a').then(->
      throw new Error('Expected stale response to be rejected')
    , (error) ->
      assert.equal(error.code, 'NATIVE_DISCONNECTED')
      assert.equal(Profiles.tunnelProxy(tunnel).port, 9)
    )
