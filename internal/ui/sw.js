// mise service worker. It exists so browsers offer to install the app; it
// caches nothing and handles no fetches, because mise needs a connection.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));
