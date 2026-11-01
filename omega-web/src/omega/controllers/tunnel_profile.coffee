angular.module('omega').controller 'TunnelProfileCtrl', ($scope, $interval,
  $window, $q, omegaTarget, trFilter) ->
  # Configuration text belongs to this editor only, never to profile/options.
  editor = $scope.tunnelEditor =
    config: ''
    busy: false
    state: 'disconnected'
    error: ''
    helperUnavailable: false
    configMissing: false
  destroyed = false
  maxConfigBytes = 256 * 1024
  helperErrors = ['NATIVE_UNAVAILABLE', 'NATIVE_DISCONNECTED',
    'NATIVE_TIMEOUT', 'VERSION_MISMATCH']

  showError = (error) ->
    return if destroyed
    editor.state = 'error'
    editor.helperUnavailable = error?.code in helperErrors
    editor.configMissing = true if error?.code == 'config_not_found'
    editor.error = if editor.helperUnavailable
      trFilter('options_tunnel_helperUnavailable')
    else if error?.code == 'config_not_found'
      trFilter('options_tunnel_configMissing')
    else
      error?.message || trFilter('options_tunnel_operationFailed')

  request = (command, fields = {}, onSuccess, onFailure) ->
    return $q.when() if destroyed or editor.busy
    editor.busy = true
    editor.error = ''
    $q.when(omegaTarget.tunnelRequest(command, fields)).then((result) ->
      return if destroyed
      editor.helperUnavailable = false
      editor.configMissing = false
      editor.state = result.state if result.state
      editor.error = result.error || ''
      onSuccess?(result)
      result
    , (error) ->
      return if destroyed
      showError(error) unless onFailure?(error)
    ).finally ->
      editor.busy = false unless destroyed

  $scope.tunnelStatusKey = -> 'options_tunnel_state_' + editor.state

  $scope.tunnelRefresh = ->
    configId = $scope.profile.tunnel?.configId
    if configId
      request('status', {configId: configId})
    else
      request('version', {}, (result) ->
        if result.protocolVersion != 1
          showError({code: 'VERSION_MISMATCH'})
        else
          editor.state = 'disconnected'
      )

  $scope.tunnelLoad = (content) ->
    return if editor.busy or destroyed
    editor.error = ''
    editor.config = content

  $scope.tunnelFileError = (error) ->
    key = if error?.code == 'FILE_TOO_LARGE'
      'options_tunnel_configTooLarge'
    else
      'options_tunnel_fileError'
    editor.error = trFilter(key)

  $scope.tunnelCanImport = ->
    not editor.busy and editor.config.trim().length > 0

  $scope.tunnelImport = ->
    return unless $scope.tunnelCanImport()
    # Blob.size measures UTF-8 bytes, matching the native protocol limit.
    if new Blob([editor.config]).size > maxConfigBytes
      editor.error = trFilter('options_tunnel_configTooLarge')
      return
    fields = {config: editor.config}
    # A backup from another machine has metadata but no local DPAPI config.
    # Create a fresh native config only after an explicit not-found response.
    if $scope.profile.tunnel? and not editor.configMissing
      fields.configId = $scope.profile.tunnel.configId
    request('import', fields, (result) ->
      # Whitelist metadata: native responses cannot put secrets in backups.
      $scope.profile.tunnel =
        configId: result.configId
        protocol: result.protocol
        port: result.port
      editor.config = ''
    )

  $scope.tunnelConnect = ->
    configId = $scope.profile.tunnel?.configId
    request('connect', {configId: configId}) if configId

  $scope.tunnelDisconnect = ->
    configId = $scope.profile.tunnel?.configId
    request('disconnect', {configId: configId}) if configId

  $scope.tunnelRemove = ->
    configId = $scope.profile.tunnel?.configId
    return unless configId and not editor.busy
    # Duplicated profiles can reference the same protected configuration.
    unless editor.configMissing
      for key, profile of $scope.options when key.charAt(0) == '+'
        if profile != $scope.profile and profile.tunnel?.configId == configId
          editor.error = trFilter('options_tunnel_configReferenced')
          return
    return unless $window.confirm(trFilter('options_tunnel_removeConfirm'))
    clearReference = ->
      delete $scope.profile.tunnel
      editor.state = 'disconnected'
      editor.config = ''
      editor.error = ''
      editor.configMissing = false
    if editor.configMissing
      # Forget only this stale reference; never delete a config elsewhere.
      clearReference()
      return
    request('delete', {configId: configId}, clearReference, (error) ->
      return false unless error?.code == 'config_not_found'
      clearReference()
      true
    )

  # Status polling is stopped with the editor and cannot retain config text.
  poll = $interval((->
    $scope.tunnelRefresh() if $scope.profile.tunnel?.configId
  ), 5000)
  $scope.$on '$destroy', ->
    destroyed = true
    editor.config = ''
    $interval.cancel(poll)
  $scope.tunnelRefresh()
