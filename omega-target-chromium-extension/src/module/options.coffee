OmegaTarget = require('omega-target')
OmegaPac = OmegaTarget.OmegaPac
Promise = OmegaTarget.Promise
querystring = require('querystring')
WebRequestMonitor = require('./web_request_monitor')
ChromePort = require('./chrome_port')
fetchUrl = require('./fetch_url')
Url = require('url')
TunnelClient = require('./tunnel_client')

TEMPPROFILEKEY = 'tempProfileState'

class ChromeOptions extends OmegaTarget.Options
  _inspect: null
  _actionForUrl: null

  fetchUrl: fetchUrl
  _networkInspectPorts: []

  constructor: (args...) ->
    super(args...)

    chrome.runtime.onConnect.addListener((port) =>
      return if port.name isnt 'network-inspect'
      unless @_requestMonitor
        originalEnabled = @_monitorWebRequests
        @setMonitorWebRequests(true)
        @_monitorWebRequests = originalEnabled
      port.postMessage({
        type: 'connected'
      })
      onMessage = (msg) =>
        switch msg.type
          when 'init'
            requestTabId = msg.tabId
            _tabInfo = @_requestMonitor.tabInfo
            if requestTabId
              _tabInfo = {}
              _tabInfo[requestTabId] = @_requestMonitor.tabInfo[requestTabId]
            port.postMessage({
              type: 'init'
              data: _tabInfo
            })
      port.onMessage.addListener(onMessage)
      @_networkInspectPorts.push(port)
      disConnect = =>
        port.onMessage.removeListener(onMessage)
        port.onDisconnect.removeListener(disConnect)
        pos = 0
        while pos >= 0
          pos = @_networkInspectPorts.indexOf(port)
          if pos >= 0
            @_networkInspectPorts.splice(pos, 1)
      port.onDisconnect.addListener(disConnect)
    )

    chrome.alarms.onAlarm.addListener (alarm) =>
      switch alarm.name
        when 'omega.tunnelsReconnect'
          @ready.then => @_connectActiveTunnels()
        when 'omega.updateProfile'
          @ready.then( =>
            @updateProfile()
          )
    chrome.contextMenus?.onClicked.addListener((info, tab) =>
      @ready.then( =>
        switch info.menuItemId
          when 'enableQuickSwitch'
            changes = {}
            changes['-enableQuickSwitch'] = info.checked
            setOptions = @_setOptions(changes)
            if info.checked and not @_quickSwitchCanEnable
              setOptions.then ->
                chrome.tabs.create(
                  url: chrome.runtime.getURL('options.html#/ui')
                )
      )

    )

    chrome.action.onClicked.addListener (tab) =>
      if browser?.proxy?.onRequest?
        browser.permissions.request({origins: ["<all_urls>"]})
      @ready.then( =>
        @clearBadge()
        if not @_options['-enableQuickSwitch']
          # If we reach here, then the browser does not support popup.
          # Let's open the popup page in a tab.
          chrome.tabs.create(url: 'popup/index.html')
          return
        profiles = @_options['-quickSwitchProfiles']
        index = profiles.indexOf(@_currentProfileName)
        index = (index + 1) % profiles.length
        @applyProfile(profiles[index]).then =>
          if @_options['-refreshOnProfileChange']
            url = tab.pendingUrl or tab.url
            return if not url
            return if url.substr(0, 6) == 'chrome'
            return if url.substr(0, 6) == 'about:'
            return if url.substr(0, 4) == 'moz-'
            if tab.pendingUrl
              chrome.tabs.update(tab.id, {url: url})
            else
              chrome.tabs.reload(tab.id)
      )

  # This method is exposed only to our options page by background.coffee.
  # Config text travels directly to the native host, never through options,
  # request logs, browser storage, sync or export.
  tunnelRequest: (command, fields = {}) ->
    unless @_tunnelClient
      @_tunnelClient = new TunnelClient(chrome.runtime, Promise)
      @_tunnelClient.onDisconnect = =>
        @_tunnelConnectionGeneration = (@_tunnelConnectionGeneration ? 0) + 1
        if OmegaPac.Profiles.clearTunnelVerifications()
          @_reapplyTunnelPolicy().catch (error) => @log.error(error)
    @_pausedTunnels ?= {}
    @_tunnelOperationGeneration ?= {}
    if fields.configId
      if command == 'disconnect'
        @_pausedTunnels[fields.configId] = true
      else if command == 'connect'
        delete @_pausedTunnels[fields.configId]
      if command in ['import', 'disconnect', 'delete']
        @_tunnelOperationGeneration[fields.configId] =
          (@_tunnelOperationGeneration[fields.configId] ? 0) + 1
    generation = @_tunnelOperationGeneration[fields.configId] ? 0
    revoked = false
    if fields.configId and command in ['import', 'disconnect', 'delete']
      revoked = OmegaPac.Profiles.setTunnelVerification(fields.configId, null)
    ready = if revoked then @_reapplyTunnelPolicy() else Promise.resolve()
    connectionGeneration = null
    ready.then(=>
      connectionGeneration = @_tunnelConnectionGeneration ? 0
      @_tunnelClient.request(command, fields)
    ).then (result) =>
      if connectionGeneration != (@_tunnelConnectionGeneration ? 0)
        error = new Error('The Windows helper disconnected. Try again.')
        error.code = 'NATIVE_DISCONNECTED'
        throw error
      if command == 'version' and result.protocolVersion != 1
        error = new Error('Update the ZeroOmega WG Windows helper.')
        error.code = 'VERSION_MISMATCH'
        throw error
      if command in ['connect', 'status']
        # A response from before Disconnect or a replacement import cannot
        # authorize that stale configuration again.
        return result if generation !=
          (@_tunnelOperationGeneration[fields.configId] ? 0)
        if result.configId != fields.configId
          throw new Error('The helper returned a different configuration.')
        changed = OmegaPac.Profiles.setTunnelVerification(fields.configId,
          result)
        return @_reapplyTunnelPolicy().then(-> result) if changed
      return result
    .catch (error) =>
      throw error if generation !=
        (@_tunnelOperationGeneration[fields.configId] ? 0)
      if fields.configId and
          OmegaPac.Profiles.setTunnelVerification(fields.configId, null)
        return @_reapplyTunnelPolicy().then -> throw error
      throw error

  _reapplyTunnelPolicy: ->
    meta = @currentProfile()
    return Promise.resolve() unless meta
    profile = if @_tempProfileActive then @_tempProfile else meta
    @proxyImpl.applyProfile(profile, meta, @_options)

  applyProfile: (name, args) ->
    # Install the fail-closed proxy policy before starting any network work.
    super(name, args).then (result) =>
      return result if args?.proxy == false
      @_connectActiveTunnels().then -> result

  _connectActiveTunnels: ->
    profile = if @_tempProfileActive then @_tempProfile else @currentProfile()
    return Promise.resolve() unless profile
    references = OmegaPac.Profiles.allReferenceSet(profile, @_options,
      profileNotFound: @_profileNotFound.bind(this))
    pending = []
    seen = {}
    errors = {}
    for own key, name of references
      tunnelProfile = OmegaPac.Profiles.byKey(key, @_options)
      continue unless tunnelProfile?.profileType == 'TunnelProfile'
      continue unless OmegaPac.Profiles.validTunnelReference(tunnelProfile)
      tunnel = tunnelProfile.tunnel
      continue if @_pausedTunnels?[tunnel.configId]
      continue if seen[tunnel.configId]
      seen[tunnel.configId] = true
      do (tunnel) =>
        pending.push(@tunnelRequest('connect', configId: tunnel.configId)
          .then (result) ->
            if result.port != tunnel.port or result.protocol != tunnel.protocol
              throw new Error('Reimport this configuration on this computer.')
          .catch (error) ->
            errors[tunnel.configId] = error.message
        )
    if pending.length
      chrome.alarms.create('omega.tunnelsReconnect', periodInMinutes: 1)
    else
      chrome.alarms.clear('omega.tunnelsReconnect')
    # Do not disconnect profiles on switch: the per-user broker can also be
    # serving another browser. Explicit Disconnect remains available in UI.
    Promise.all(pending).then => @_state.set(tunnelErrors: errors)

  init: (startupCheck) ->
    super(startupCheck)
    @ready.then =>
      chrome.storage.session
      .get(TEMPPROFILEKEY).then((tempProfileState = {}) =>
        tempProfileState = tempProfileState[TEMPPROFILEKEY]
        # tempProfileState =
        # { _tempProfile,
        # _tempProfileActive}
        if tempProfileState
          @_tempProfile = tempProfileState._tempProfile
          @_tempProfile.rules.forEach((_rule) =>
            key = OmegaPac.Profiles.nameAsKey(_rule.profileName)
            @_tempProfileRulesByProfile[key] =
              @_tempProfileRulesByProfile[key] or []
            @_tempProfileRulesByProfile[key].push(_rule)
            condition = _rule.condition
            domain = condition.pattern.substring(2)
            @_tempProfileRules[domain] = _rule
          )
          #@_tempProfileRules = tempProfileState._tempProfileRules
          #@_tempProfileRulesByProfile =
          #  tempProfileState._tempProfileRulesByProfile
          @_tempProfileActive = tempProfileState._tempProfileActive
          OmegaPac.Profiles.updateRevision(@_tempProfile)
          console.log('apply temp state profile', @_currentProfileName)
          @applyProfile(@_currentProfileName)
        )
    @ready

  addTempRule: (domain, profileName, toggle) ->
    super(domain, profileName, toggle).then =>
      _zeroState = {}
      _zeroState[TEMPPROFILEKEY] = {
        _tempProfile: @_tempProfile
        _tempProfileActive: @_tempProfileActive
      }
      chrome.storage.session.set(_zeroState)
  updateProfile: (args...) ->
    super(args...).then (results) ->
      error = false
      for own profileName, result of results
        if result instanceof Error
          error = true
          break
      if error
        # TODO(catus): Find a better way to notify the user.
        ###
        @setBadge(
          text: '!'
          color: '#faa732'
          title: chrome.i18n.getMessage('browserAction_titleDownloadFail')
        )
        ###
      return results

  _proxyNotControllable: null
  proxyNotControllable: -> @_proxyNotControllable
  setProxyNotControllable: (reason, badge) ->
    @_proxyNotControllable = reason
    if reason
      @_state.set({'proxyNotControllable': reason})
      @setBadge(badge)
    else
      @_state.remove(['proxyNotControllable'])
      @clearBadge()

  _badgeTitle: null
  setBadge: (options) ->
    if not options
      options =
        if @_proxyNotControllable
          text: '='
          color: '#da4f49'
        else
          text: '?'
          color: '#49afcd'
    chrome.action.setBadgeText(text: options.text)
    chrome.action.setBadgeBackgroundColor(color: options.color)
    if options.title
      @_badgeTitle = options.title
      chrome.action.setTitle(title: options.title)
    else
      @_badgeTitle = null
  clearBadge: ->
    return if @externalApi.disabled
    if @_badgeTitle
      @currentProfileChanged('clearBadge')
    if @_proxyNotControllable
      @setBadge()
    else
      chrome.action.setBadgeText?(text: '')
    return

  _quickSwitchCanEnable: false
  setQuickSwitch: (quickSwitch, canEnable) ->
    @_quickSwitchCanEnable = canEnable
    if quickSwitch
      chrome.action.setPopup({popup: ''})
    else
      chrome.action.setPopup({popup: globalThis.POPUPHTMLURL})

    chrome.contextMenus?.update('enableQuickSwitch', {checked: !!quickSwitch})
    Promise.resolve()

  setInspect: (settings) ->
    if @_inspect
      if settings.showMenu
        @_inspect.enable()
      else
        @_inspect.disable()
    return Promise.resolve()

  _requestMonitor: null
  _monitorWebRequests: false
  _tabRequestInfoPorts: null
  setMonitorWebRequests: (enabled) ->
    @_monitorWebRequests = enabled
    if enabled and not @_requestMonitor?
      @_tabRequestInfoPorts = {}
      wildcardForReq = (req) -> OmegaPac.wildcardForUrl(req.url)
      @_requestMonitor = new WebRequestMonitor(wildcardForReq)
      @_requestMonitor.watchTabs (tabId, info, req, status) =>
        unless /^(chrome|moz)-extension:\/\//i.test(req?.url)
          updateMessage = =>
            @_networkInspectPorts.forEach((port) ->
              port.postMessage({
                type: 'update'
                data: {
                  tabId,
                  info,
                  req,
                  status
                }
              })
            )
          if status is 'start'
            @_actionForUrl(req.url, {skipIcon: true})
              .then((action) ->
                request = info.requests[req.requestId]
                if request
                  request.actionProfile = action
                  updateMessage()
              )
          updateMessage()
        return unless @_monitorWebRequests
        if info.errorCount > 0
          info.badgeSet = true
          badge = {text: info.errorCount.toString(), color: '#f0ad4e'}
          chrome.action.setBadgeText(text: badge.text, tabId: tabId)
          .catch((e) ->
            console.log('error:',e)
          )
          chrome.action.setBadgeBackgroundColor(
            color: badge.color
            tabId: tabId
          ).catch((e) ->
            console.log('error:',e)
          )
        else if info.badgeSet
          info.badgeSet = false
          chrome.action.setBadgeText(text: '', tabId: tabId)
          .catch((e) ->
            console.log('error:',e)
          )
        @_tabRequestInfoPorts[tabId]?.postMessage({
          errorCount: info.errorCount
          summary: info.summary
        })

      chrome.runtime.onConnect.addListener (rawPort) =>
        return unless rawPort.name == 'tabRequestInfo'
        return unless @_monitorWebRequests
        tabId = null
        port = new ChromePort(rawPort)
        port.onMessage.addListener (msg) =>
          tabId = msg.tabId
          @_tabRequestInfoPorts[tabId] = port
          info = @_requestMonitor.tabInfo[tabId]
          if info
            port.postMessage({
              errorCount: info.errorCount
              summary: info.summary
            })
        port.onDisconnect.addListener =>
          delete @_tabRequestInfoPorts[tabId] if tabId?

  schedule: (name, periodInMinutes) ->
    name = 'omega.' + name
    if periodInMinutes < 0
      chrome.alarms.clear(name)
    else
      chrome.alarms.create(name, {
        periodInMinutes: periodInMinutes
      })
    Promise.resolve()

  printFixedProfile: (profile) ->
    return unless profile.profileType == 'FixedProfile'
    result = ''
    for scheme in OmegaPac.Profiles.schemes when profile[scheme.prop]
      pacResult = OmegaPac.Profiles.pacResult(profile[scheme.prop])
      if scheme.scheme
        result += "#{scheme.scheme}: #{pacResult}\n"
      else
        result += "#{pacResult}\n"
    result ||= chrome.i18n.getMessage(
      'browserAction_profileDetails_DirectProfile')
    return result

  printProfile: (profile) ->
    type = profile.profileType
    if type.indexOf('RuleListProfile') >= 0
      type = 'RuleListProfile'

    if type == 'TunnelProfile'
      protocol = if profile.tunnel?.protocol == 'amneziawg'
        'AmneziaWG'
      else
        'WireGuard'
      return protocol + ' — local Windows helper'
    else if type == 'FixedProfile'
      @printFixedProfile(profile)
    else if type == 'PacProfile' and profile.pacUrl
      profile.pacUrl
    else
      chrome.i18n.getMessage('browserAction_profileDetails_' + type) || null

  upgrade: (options, changes) ->
    super(options).catch (err) ->
      return Promise.reject err if options?['schemaVersion']
      return Promise.reject new OmegaTarget.Options.NoOptionsError()

  onFirstRun: (reason) ->
    console.log('first run ....')
    chrome.tabs.create url: chrome.runtime.getURL('options.html')

  getPageInfo: ({tabId, url}) ->
    errorCount = @_requestMonitor?.tabInfo[tabId]?.errorCount
    result = if errorCount then {errorCount: errorCount} else null
    getBadge = new Promise (resolve, reject) ->
      if not chrome.action.getBadgeText?
        resolve('')
        return
      chrome.action.getBadgeText {tabId: tabId}, (result) ->
        resolve(result)

    getInspectUrl = @_state.get({inspectUrl: ''})
    Promise.join getBadge, getInspectUrl, (badge, {inspectUrl}) =>
      if badge == '#' and inspectUrl
        url = inspectUrl
      else
        @clearBadge()
      return result if not url
      if url.substr(0, 6) == 'chrome'
        errorPagePrefix = 'chrome://errorpage/'
        if url.substr(0, errorPagePrefix.length) == errorPagePrefix
          url = querystring.parse(url.substr(url.indexOf('?') + 1)).lasturl
          return result if not url
        else
          return result
      return result if url.substr(0, 6) == 'about:'
      return result if url.substr(0, 4) == 'moz-'
      domain = OmegaPac.getBaseDomain(Url.parse(url).hostname)
      subdomain = OmegaPac.getSubdomain(url)

      return {
        url: url
        domain: domain
        subdomain: subdomain
        tempRuleProfileName: @queryTempRule(domain)
        errorCount: errorCount
      }

module.exports = ChromeOptions
