const CACHE_NAME = "searchplus2026-pwa-v1";

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method === "POST" && url.searchParams.has("share-target")) {
    event.respondWith(
      (async () => {
        try {
          const formData = await event.request.formData();
          const files = formData.getAll("media");
          const target = files.length > 0 ? files[0] : formData.get("media");
          if (target && target instanceof File) {
            const cache = await caches.open("searchplus2026-share-cache");
            await cache.put(
              "/_pwa_shared_media_",
              new Response(target, {
                headers: {
                  "content-type": target.type || "image/jpeg",
                  "x-shared-name": encodeURIComponent(target.name || "shared.jpg"),
                },
              })
            );
          }
        } catch (err) {}
        return Response.redirect("./?shared=1", 303);
      })()
    );
  }
});

self.addEventListener("install", (event) => {
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener("fetch", (event) => {
  if (event.request.method !== "GET") return;
  event.respondWith(
    fetch(event.request).catch(() => caches.match(event.request))
  );
});

