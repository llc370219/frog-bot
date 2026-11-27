const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('frogDesktop', {
  getState: () => ipcRenderer.invoke('frog:get-state'),
  setModal: (open) => ipcRenderer.invoke('frog:set-modal', open),
  setDrawer: (open) => ipcRenderer.invoke('frog:set-drawer', open),
  setTheme: (theme) => ipcRenderer.invoke('frog:set-theme', theme),
  setPref: (pref) => ipcRenderer.invoke('frog:set-pref', pref),
  selectServer: (id) => ipcRenderer.invoke('frog:select-server', id),
  saveServer: (payload) => ipcRenderer.invoke('frog:save-server', payload),
  removeServer: (id) => ipcRenderer.invoke('frog:remove-server', id),
  reloadServer: (id) => ipcRenderer.invoke('frog:reload-server', id),
  logoutServer: (id) => ipcRenderer.invoke('frog:logout-server', id),
  navServer: (payload) => ipcRenderer.invoke('frog:nav-server', payload),
  toggleRelay: (payload) => ipcRenderer.invoke('frog:relay-toggle', payload),
  setupLocal: () => ipcRenderer.invoke('frog:local-setup'),
  discoverLocal: () => ipcRenderer.invoke('frog:discover-local'),
  checkUpdates: () => ipcRenderer.invoke('frog:check-updates'),
  checkServerUpdate: (payload) => ipcRenderer.invoke('frog:server-check-update', payload),
  applyServerUpdate: (payload) => ipcRenderer.invoke('frog:server-apply-update', payload),
  copyText: (text) => ipcRenderer.invoke('frog:copy-text', text),
  openExternal: (url) => ipcRenderer.invoke('frog:open-external', url),
  onState: (cb) => {
    const handler = (_event, state) => cb(state);
    ipcRenderer.on('frog:state', handler);
    return () => ipcRenderer.removeListener('frog:state', handler);
  },
});
