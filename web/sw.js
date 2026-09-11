const CACHE_NAME = "submanager-static-v1";
const STATIC_ASSETS = [
  "/assets/app.css?v=20260911-integrations-pwa",
  "/assets/app.js?v=20260911-integrations-pwa",
  "/manifest.webmanifest",
  "/icon.svg",
];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE_NAME).then((cache) => cache.addAll(STATIC_ASSETS)));
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(
      keys.filter((key) => key.startsWith("submanager-static-") && key !== CACHE_NAME)
        .map((key) => caches.delete(key)),
    )),
  );
  self.clients.claim();
});

self.addEventListener("fetch", (event) => {
  const requestURL = new URL(event.request.url);
  if (event.request.method !== "GET" || requestURL.origin !== self.location.origin) return;
  if (!requestURL.pathname.startsWith("/assets/") &&
      requestURL.pathname !== "/manifest.webmanifest" && requestURL.pathname !== "/icon.svg") return;
  event.respondWith(caches.match(event.request).then((cached) => cached || fetch(event.request)));
});
