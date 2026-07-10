export function getAuthHeader() {
  const key = localStorage.getItem('minimax2api_proxy_key') || 'sk-minimax';
  return { Authorization: `Bearer ${key}` };
}
