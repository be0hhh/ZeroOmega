chai = require 'chai'
Profiles = require '../src/profiles'
PacGenerator = require '../src/pac_generator'
Conditions = require '../src/conditions'

describe 'TunnelProfile routing', ->
  beforeEach ->
    Profiles.clearTunnelVerifications()
    Profiles.setTunnelVerification('test-config', {
      configId: 'test-config', protocol: 'wireguard', port: 32123
      state: 'connecting'
    })
  afterEach -> Profiles.clearTunnelVerifications()
  tunnelProfile = (port = 32123) ->
    name: 'vpn'
    profileType: 'TunnelProfile'
    tunnel: {configId: 'test-config', protocol: 'wireguard', port: port}

  it 'routes selected domains through the tunnel and all others directly', ->
    options =
      '+vpn': tunnelProfile()
      '+auto': Profiles.create({
        name: 'auto'
        profileType: 'SwitchProfile'
        defaultProfileName: 'direct'
        rules: [{profileName: 'vpn', condition: {
          conditionType: 'HostWildcardCondition', pattern: '*.example.com'
        }}]
      })
    ast = PacGenerator.compress(PacGenerator.script(options, 'auto'))
    pac = ast.print_to_string()
    func = eval("(function () { #{pac}; return FindProxyForURL; })()")
    chai.assert.equal(func('https://app.example.com/', 'app.example.com'),
      'SOCKS5 127.0.0.1:32123')
    chai.assert.equal(func('https://other.example.org/', 'other.example.org'),
      'DIRECT')

  it 'never adds DIRECT or legacy SOCKS fallback with compatibility on', ->
    previous = globalThis.FORCEFIXEXPORTSCRIPTFORSOCKS
    globalThis.FORCEFIXEXPORTSCRIPTFORSOCKS = true
    try
      profile = tunnelProfile()
      chai.assert.equal(Profiles.match(profile,
        Conditions.requestFromUrl('https://example.com/'))[0],
        'SOCKS5 127.0.0.1:32123')
      chai.assert.equal(Profiles.compile(profile).value,
        'SOCKS5 127.0.0.1:32123')
    finally
      globalThis.FORCEFIXEXPORTSCRIPTFORSOCKS = previous

  it 'blocks missing and malformed configuration references', ->
    for port in [undefined, null, '1080', 0, 9, -1, 65536, 1080.5, NaN]
      profile = tunnelProfile(port)
      # A default function parameter fills undefined; explicitly overwrite it.
      profile.tunnel.port = port
      chai.assert.equal(Profiles.compile(profile).value, 'SOCKS5 127.0.0.1:9')
    invalidTunnels = [undefined, {}, {
      port: 1080, configId: '../secret', protocol: 'wireguard'
    }, {port: 1080, configId: 'valid', protocol: 'http'}]
    for tunnel in invalidTunnels
      profile = tunnelProfile()
      profile.tunnel = tunnel
      chai.assert.equal(Profiles.compile(profile).value, 'SOCKS5 127.0.0.1:9')
    profile = tunnelProfile()
    profile.tunnel.host = 'attacker.example'
    chai.assert.equal(Profiles.tunnelProxy(profile).host, '127.0.0.1')

  it 'is available as auto switch result alongside existing proxy profiles', ->
    options =
      '+vpn': tunnelProfile()
      '+http': Profiles.create('http', 'FixedProfile')
      '+auto': Profiles.create('auto', 'SwitchProfile')
    names = Profiles.validResultProfilesFor(options['+auto'], options)
      .map((profile) -> profile.name)
    chai.assert.include(names, 'vpn')
    chai.assert.include(names, 'http')

  it 'blocks unverified backup ports belonging to a different local config', ->
    profile = tunnelProfile()
    Profiles.clearTunnelVerifications()
    Profiles.setTunnelVerification('other-config', {
      configId: 'other-config', protocol: 'wireguard', port: 32123
      state: 'connected'
    })
    chai.assert.equal(Profiles.compile(profile).value, 'SOCKS5 127.0.0.1:9')
    Profiles.setTunnelVerification('test-config', {
      configId: 'test-config', protocol: 'wireguard', port: 32124
      state: 'connected'
    })
    chai.assert.equal(Profiles.compile(profile).value, 'SOCKS5 127.0.0.1:9')

  it 'invalidates already compiled policies after native helper loss', ->
    options = {'+vpn': tunnelProfile()}
    compile = ->
      pac = PacGenerator.script(options, 'vpn').print_to_string()
      eval("(function () { #{pac}; return FindProxyForURL; })()")
    chai.assert.equal(compile()('https://example.com', 'example.com'),
      'SOCKS5 127.0.0.1:32123')
    Profiles.clearTunnelVerifications()
    chai.assert.equal(compile()('https://example.com', 'example.com'),
      'SOCKS5 127.0.0.1:9')
