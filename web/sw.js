const CACHE_NAME = "submanager-static-v5";
const STATIC_ASSETS = [
  "/assets/app.css?v=20260911-pwa-install-copy",
  "/assets/app.js?v=20260911-pwa-install-copy",
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

self.addEventListener("push", (event) => {
  let payload = { title: "🔔 결제 예정", body: "SubManager 결제 예정 알림이 있어요.", tag: "submanager-upcoming", url: "/" };
  try {
    payload = { ...payload, ...event.data.json() };
  } catch {}
  event.waitUntil(self.registration.showNotification(payload.title, {
    body: payload.body,
    tag: payload.tag,
    icon: "/icon.svg",
    badge: "/icon.svg",
    data: { url: payload.url },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
    const existing = windows.find((window) => new URL(window.url).origin === self.location.origin);
    return existing ? existing.focus() : clients.openWindow(event.notification.data?.url || "/");
  }));
});
