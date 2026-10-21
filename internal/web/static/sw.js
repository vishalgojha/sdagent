const CACHE = "agent-v-shell-v3-20261021";
self.addEventListener("install", event => {
  event.waitUntil(caches.open(CACHE).then(cache => cache.addAll(["/", "/app.js?v=20261021-1", "/manifest.webmanifest"])));
  self.skipWaiting();
});
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));
self.addEventListener("fetch", event => {
  if (event.request.method !== "GET") return;
  event.respondWith(fetch(event.request).catch(() => caches.match(event.request).then(r => r || caches.match("/"))));
});
