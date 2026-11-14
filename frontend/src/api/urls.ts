export function apiBaseURL() {
  return (import.meta.env.VITE_API_BASE_URL || import.meta.env.BASE_URL).replace(/\/$/, '');
}

export function documentURL(url: string) {
  return url.startsWith('/demo/docs/') ? `${import.meta.env.BASE_URL.replace(/\/$/, '')}${url}` : url;
}
