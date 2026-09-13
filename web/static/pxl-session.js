(function () {
  'use strict';

  let refreshing;
  function refresh() {
    if (!refreshing) refreshing = refreshSession().finally(() => { refreshing = null; });
    return refreshing;
  }

  async function refreshSession() {
    const response = await fetch('/api/v1/auth/browser-refresh', {
      method: 'POST',
      headers: {'Accept': 'application/json'}
    });
    if (!response.ok) return false;
    const data = await response.json();
    if (data.user) localStorage.setItem('pxl_user', JSON.stringify(data.user));
    return true;
  }

  async function request(url, options) {
    let response = await fetch(url, options || {});
    if (response.status === 401 && !String(url).includes('/auth/browser-')) {
      if (await refresh()) response = await fetch(url, options || {});
    }
    return response;
  }

  async function currentUser() {
    const response = await request('/api/v1/auth/me', {headers: {'Accept': 'application/json'}});
    if (!response.ok) {
      localStorage.removeItem('pxl_user');
      return null;
    }
    const user = await response.json();
    localStorage.setItem('pxl_user', JSON.stringify(user));
    return user;
  }

  async function logout() {
    const response = await request('/api/v1/auth/logout', {method: 'POST'});
    if (!response.ok && response.status !== 401) throw new Error('Logout failed');
    localStorage.removeItem('pxl_user');
  }

  // Remove credentials written by releases predating HttpOnly browser sessions.
  localStorage.removeItem('pxl_token');
  localStorage.removeItem('pxl_refresh');

  function escapeHTML(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
  }

  window.PXLSession = {request, refresh, currentUser, logout, escapeHTML};
}());
