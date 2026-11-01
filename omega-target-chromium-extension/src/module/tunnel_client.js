'use strict';

// No configuration bodies are logged, persisted or included in thrown errors.
function failure(code, message) {
  var err = new Error(message);
  err.code = code;
  return err;
}

function TunnelClient(runtime, PromiseImpl, timeout) {
  this.runtime = runtime;
  this.Promise = PromiseImpl;
  this.timeout = timeout || 15000;
  this.port = null;
  this.pending = Object.create(null);
  this.nextId = 0;
}

TunnelClient.prototype._connect = function () {
  if (this.port) return this.port;
  var self = this;
  var port;
  try {
    port = this.runtime.connectNative('io.github.zeroomega.wg');
  } catch (_) {
    throw failure('NATIVE_UNAVAILABLE', 'Install the ZeroOmega WG Windows helper first.');
  }
  this.port = port;
  port.onMessage.addListener(function (response) {
    var entry = response && self.pending[response.id];
    if (!entry) return;
    delete self.pending[response.id];
    clearTimeout(entry.timer);
    if (response.ok === true && response.result && typeof response.result === 'object') {
      entry.resolve(response.result);
    } else if (response.ok === false && response.error && typeof response.error.code === 'string') {
      entry.reject(failure(response.error.code,
        typeof response.error.message === 'string' ? response.error.message : 'The helper rejected this request.'));
    } else {
      entry.reject(failure('INVALID_RESPONSE', 'The helper returned an invalid response.'));
    }
  });
  port.onDisconnect.addListener(function () {
    // Reading lastError suppresses Chromium's unchecked runtime error. Its text
    // may contain paths; expose a stable, actionable message instead.
    var ignored = self.runtime.lastError;
    if (self.port !== port) return;
    self.port = null;
    Object.keys(self.pending).forEach(function (id) {
      var entry = self.pending[id];
      clearTimeout(entry.timer);
      entry.reject(failure('NATIVE_DISCONNECTED',
        'The Windows helper is unavailable. Install it, then try again.'));
      delete self.pending[id];
    });
    if (self.onDisconnect) self.onDisconnect();
  });
  return port;
};

TunnelClient.prototype.request = function (command, fields) {
  var self = this;
  fields = fields || {};
  return new this.Promise(function (resolve, reject) {
    var allowed = ['version', 'import', 'connect', 'disconnect', 'status', 'delete'];
    if (allowed.indexOf(command) < 0 || typeof fields !== 'object') {
      reject(failure('INVALID_REQUEST', 'Unsupported tunnel operation.'));
      return;
    }
    if (((command !== 'version' && command !== 'import') || fields.configId !== undefined) &&
        (typeof fields.configId !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(fields.configId))) {
      reject(failure('INVALID_REQUEST', 'Import a configuration first.'));
      return;
    }
    if (command === 'import' &&
        (typeof fields.config !== 'string' || fields.config.length > 262144)) {
      reject(failure('INVALID_REQUEST', 'Select a .conf file smaller than 256 KiB.'));
      return;
    }
    var port;
    try { port = self._connect(); } catch (err) { reject(err); return; }
    var id = String(++self.nextId);
    var request = {id: id, command: command};
    if (fields.configId !== undefined) request.configId = fields.configId;
    if (command === 'import') request.config = fields.config;
    self.pending[id] = {
      resolve: resolve,
      reject: reject,
      timer: setTimeout(function () {
        delete self.pending[id];
        reject(failure('NATIVE_TIMEOUT', 'The Windows helper did not respond. Try again.'));
      }, self.timeout)
    };
    try { port.postMessage(request); } catch (_) {
      if (self.pending[id]) {
        clearTimeout(self.pending[id].timer);
        delete self.pending[id];
      }
      reject(failure('NATIVE_DISCONNECTED', 'The Windows helper disconnected. Try again.'));
    }
  });
};

module.exports = TunnelClient;
