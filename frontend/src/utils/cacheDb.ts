const DB_NAME = "immich_mine_search_db";
const DB_VERSION = 1;

let dbPromise: Promise<IDBDatabase> | null = null;

function getDb(): Promise<IDBDatabase> {
  if (!dbPromise) {
    dbPromise = new Promise((resolve, reject) => {
      const req = indexedDB.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = () => {
        const db = req.result;
        if (!db.objectStoreNames.contains("thumbnails")) {
          db.createObjectStore("thumbnails", { keyPath: "assetId" });
        }
        if (!db.objectStoreNames.contains("query_cache")) {
          db.createObjectStore("query_cache", { keyPath: "cacheKey" });
        }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }
  return dbPromise;
}

export async function getCachedThumbnail(assetId: string): Promise<Blob | null> {
  try {
    const db = await getDb();
    return new Promise((resolve) => {
      const tx = db.transaction("thumbnails", "readonly");
      const store = tx.objectStore("thumbnails");
      const req = store.get(assetId);
      req.onsuccess = () => resolve(req.result?.blob || null);
      req.onerror = () => resolve(null);
    });
  } catch {
    return null;
  }
}

export async function setCachedThumbnail(assetId: string, blob: Blob): Promise<void> {
  try {
    const db = await getDb();
    const tx = db.transaction("thumbnails", "readwrite");
    const store = tx.objectStore("thumbnails");
    store.put({ assetId, blob, time: Date.now() });
  } catch {}
}

export async function getCachedQuery(cacheKey: string): Promise<any | null> {
  try {
    const db = await getDb();
    return new Promise((resolve) => {
      const tx = db.transaction("query_cache", "readonly");
      const store = tx.objectStore("query_cache");
      const req = store.get(cacheKey);
      req.onsuccess = () => resolve(req.result?.data || null);
      req.onerror = () => resolve(null);
    });
  } catch {
    return null;
  }
}

export async function setCachedQuery(cacheKey: string, data: any): Promise<void> {
  try {
    const db = await getDb();
    const tx = db.transaction("query_cache", "readwrite");
    const store = tx.objectStore("query_cache");
    store.put({ cacheKey, data, time: Date.now() });
  } catch {}
}

export async function deleteCachedQuery(cacheKey: string): Promise<void> {
  try {
    const db = await getDb();
    const tx = db.transaction("query_cache", "readwrite");
    const store = tx.objectStore("query_cache");
    store.delete(cacheKey);
  } catch {}
}

export async function clearAllQueryCache(): Promise<void> {
  try {
    const db = await getDb();
    const tx = db.transaction("query_cache", "readwrite");
    const store = tx.objectStore("query_cache");
    store.clear();
  } catch {}
}
