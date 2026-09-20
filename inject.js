(function () {
  if (window.self !== window.top) return;
  if (window.__IMMICH_MINE_SEARCH_INJECTED__) return;
  window.__IMMICH_MINE_SEARCH_INJECTED__ = true;

  function showDebugToast(msg) {
    const toast = document.createElement("div");
    toast.id = "immich-mine-search-toast";
    toast.style.cssText =
      "position:fixed;top:18px;right:18px;z-index:999999;padding:10px 16px;border-radius:10px;font-size:12px;font-weight:600;display:flex;align-items:center;gap:8px;box-shadow:0 8px 24px rgba(0,0,0,0.35);color:#ffffff;background:#10b981;transition:opacity 0.3s ease;pointer-events:none;";
    toast.innerHTML = `<span>✓</span><span>${msg}</span>`;
    document.body.appendChild(toast);
    setTimeout(() => {
      toast.style.opacity = "0";
      setTimeout(() => toast.remove(), 400);
    }, 4500);
  }

  let isSearchActive = false;
  let hasPushedDockHistory = false;
  let isLightboxActive = false;
  let expectingLightboxPop = false;
  let dockEl = null;
  let iframeEl = null;
  let lastMainElement = null;
  let originalMainDisplay = "";
  let pendingPastePayload = null;
  let lastActivePhotoId = null;

  function getMainElement() {
    return (
      document.querySelector("main") ||
      document.querySelector("#main-content") ||
      document.querySelector('[role="main"]')
    );
  }

  function ensureDock() {
    if (dockEl) return dockEl;
    dockEl = document.createElement("div");
    dockEl.id = "immich-mine-search-dock";
    const isDark = document.documentElement.classList.contains("dark");
    dockEl.style.cssText =
      "display:none;width:100%;height:100%;flex:1;overflow:hidden;position:relative;z-index:1;";

    iframeEl = document.createElement("iframe");
    iframeEl.id = "immich-mine-search-iframe";
    iframeEl.src = `/mine-search/?embedded=true&theme=${isDark ? "dark" : "light"}`;
    iframeEl.style.cssText =
      "width:100%;height:100%;border:none;display:block;background:transparent;";

    iframeEl.addEventListener("load", () => {
      if (pendingPastePayload && iframeEl.contentWindow) {
        iframeEl.contentWindow.postMessage(pendingPastePayload, "*");
        pendingPastePayload = null;
      }
      if (isSearchActive && iframeEl.contentWindow) {
        try {
          iframeEl.contentWindow.focus();
        } catch (_) {}
      }
    });

    dockEl.appendChild(iframeEl);
    return dockEl;
  }

  function syncThemeToIframe() {
    const isDark = document.documentElement.classList.contains("dark");
    sendToIframe({ type: "IMMICH_THEME_CHANGE", isDark });
  }

  function openSearchView() {
    const main = getMainElement();
    if (!main || !main.parentElement) return;
    ensureDock();
    if (dockEl.parentElement !== main.parentElement) {
      main.parentElement.insertBefore(dockEl, main);
    }
    syncThemeToIframe();
    lastMainElement = main;
    originalMainDisplay = main.style.display || "";
    main.style.display = "none";
    dockEl.style.display = "flex";
    isSearchActive = true;
    if (!hasPushedDockHistory) {
      try {
        history.pushState({ __immich_search__: "dock" }, "", window.location.href);
        hasPushedDockHistory = true;
      } catch (_) {}
    }
    try {
      if (document.activeElement && typeof document.activeElement.blur === "function") {
        document.activeElement.blur();
      }
      if (iframeEl && iframeEl.contentWindow) {
        iframeEl.contentWindow.focus();
      }
    } catch (_) {}
    setTimeout(() => {
      try {
        if (iframeEl && iframeEl.contentWindow) {
          iframeEl.contentWindow.focus();
        }
      } catch (_) {}
    }, 60);
  }

  function closeSearchView(fromPopState) {
    if (!isSearchActive && (!dockEl || dockEl.style.display === "none")) return;
    const main = getMainElement() || lastMainElement;
    if (dockEl) dockEl.style.display = "none";
    if (main) {
      main.style.display = originalMainDisplay === "none" ? "" : originalMainDisplay;
    }
    isSearchActive = false;
    document.body.style.overflow = "";
    document.documentElement.style.overflow = "";
    window.dispatchEvent(new Event("resize"));
    if (main) main.dispatchEvent(new Event("scroll"));
    setTimeout(() => {
      window.dispatchEvent(new Event("resize"));
      if (main) main.dispatchEvent(new Event("scroll"));
    }, 80);

    if (!fromPopState && hasPushedDockHistory) {
      hasPushedDockHistory = false;
      try {
        history.back();
      } catch (_) {}
    } else {
      hasPushedDockHistory = false;
    }
  }

  function toggleSearchView() {
    if (isSearchActive) {
      closeSearchView();
    } else {
      openSearchView();
    }
  }

  function sendToIframe(msg) {
    if (iframeEl && iframeEl.contentWindow) {
      iframeEl.contentWindow.postMessage(msg, "*");
    } else {
      pendingPastePayload = msg;
    }
  }

  function injectCameraIcon() {
    const searchInputs = document.querySelectorAll(
      '#main-search-bar, form[role="search"] input'
    );

    searchInputs.forEach((input) => {
      const form = input.closest("form") || input.parentElement;
      if (!form || form.querySelector(".immich-mine-search-btn")) return;

      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "immich-mine-search-btn";
      btn.title = "智能图像与人脸检索";
      function updateBtnPosition() {
        const hasValue = !!(input && input.value && input.value.trim().length > 0);
        btn.style.right = hasValue ? "44px" : "14px";
      }

      btn.style.cssText =
        "position:absolute;right:14px;top:50%;transform:translateY(-50%);background:none;border:none;padding:6px;cursor:pointer;display:flex;align-items:center;justify-content:center;color:#94a3b8;border-radius:9999px;z-index:10;transition:right 0.2s cubic-bezier(0.16, 1, 0.3, 1), color 0.15s ease;";

      input.addEventListener("input", updateBtnPosition);
      input.addEventListener("change", updateBtnPosition);
      updateBtnPosition();
      btn.innerHTML =
        '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/></svg>';

      btn.addEventListener("mouseenter", () => (btn.style.color = "#38bdf8"));
      btn.addEventListener("mouseleave", () => (btn.style.color = "#94a3b8"));
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        toggleSearchView();
      });

      if (window.getComputedStyle(form).position === "static") {
        form.style.position = "relative";
      }
      form.appendChild(btn);
    });

    const oldFab = document.getElementById("immich-mine-search-fab");
    if (oldFab) oldFab.remove();

    const mobileSearch = document.getElementById("search-button");
    if (mobileSearch && mobileSearch.parentNode && !document.getElementById("immich-mine-search-mobile-btn")) {
      const mBtn = document.createElement("button");
      mBtn.id = "immich-mine-search-mobile-btn";
      mBtn.type = "button";
      mBtn.className =
        "flex items-center justify-center gap-1 font-medium outline-offset-2 transition-colors focus-visible:outline-2 cursor-pointer rounded-full text-base h-10 w-10 outline-dark text-dark not-disabled:hover:bg-dark/10 sm:hidden";
      mBtn.title = "以图搜图";
      mBtn.innerHTML =
        '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/></svg>';
      mBtn.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        toggleSearchView();
      });
      mobileSearch.parentNode.insertBefore(mBtn, mobileSearch);
    }
  }

  window.addEventListener(
    "paste",
    (e) => {
      if (!isSearchActive) return;
      const items = e.clipboardData?.items;
    if (!items) return;
    for (const item of items) {
      if (item.type.startsWith("image/")) {
        e.preventDefault();
        e.stopPropagation();
        e.stopImmediatePropagation();
        const file = item.getAsFile();
        if (file) {
          const reader = new FileReader();
          reader.onload = () => {
            sendToIframe({
              type: "IMMICH_PASTE_IMAGE",
              buffer: reader.result,
              mimeType: file.type,
              fileName: file.name || "clipboard.jpg",
            });
          };
          reader.readAsArrayBuffer(file);
        }
        break;
      }
    }
  },
  true
);

  function navigateToPhoto(assetId) {
    window.open(`/photos/${encodeURIComponent(assetId)}`, "_blank");
  }

  window.addEventListener("message", (e) => {
    if (e.data?.type === "IMMICH_OPEN_PHOTO" && e.data.assetId) {
      navigateToPhoto(e.data.assetId);
    } else if (e.data?.type === "IMMICH_CLOSE_SEARCH") {
      closeSearchView(false);
    } else if (e.data?.type === "IMMICH_LIGHTBOX_OPEN") {
      if (!isLightboxActive) {
        isLightboxActive = true;
        try {
          history.pushState({ __immich_search__: "lightbox" }, "", window.location.href);
        } catch (_) {}
      }
    } else if (e.data?.type === "IMMICH_LIGHTBOX_CLOSE") {
      if (isLightboxActive) {
        isLightboxActive = false;
        expectingLightboxPop = true;
        try {
          history.back();
        } catch (_) {
          expectingLightboxPop = false;
        }
        setTimeout(() => {
          expectingLightboxPop = false;
        }, 300);
      }
    }
  });

  window.addEventListener(
    "keydown",
    (e) => {
      if (e.key === "Escape" && isSearchActive) {
        closeSearchView();
      }
    },
    true
  );

  let debounceTimer = null;
  const observer = new MutationObserver(() => {
    if (debounceTimer) return;
    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      injectCameraIcon();
      const match = window.location.pathname.match(/\/(?:photos|archive)\/([a-f0-9-]{36})/i);
      if (match && match[1] && match[1] !== lastActivePhotoId) {
        lastActivePhotoId = match[1];
      }
    }, 120);
  });
  observer.observe(document.body, { childList: true, subtree: true });
  injectCameraIcon();

  const themeObserver = new MutationObserver(() => {
    syncThemeToIframe();
  });
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });

  // 1. 捕获阶段阻断侧键事件向下渗透给大图手势监听器，彻底消除滚轮死锁
  ["pointerdown", "mousedown"].forEach((evtName) => {
    window.addEventListener(
      evtName,
      (e) => {
        if (e.button === 3 || e.button === 4) {
          e.stopPropagation();
        }
      },
      true
    );
  });

  function extractAssetIdFromTarget(target) {
    if (!target || !(target instanceof Element)) return null;
    const a = target.closest('a[href*="/photos/"]');
    if (a) {
      const m = (a.getAttribute("href") || "").match(/\/photos\/([a-f0-9-]{36})/i);
      if (m) return m[1];
    }
    const assetEl = target.closest("[data-asset-id]");
    if (assetEl) {
      const id = assetEl.getAttribute("data-asset-id");
      if (id && /[a-f0-9-]{36}/i.test(id)) return id;
    }
    const img = target.closest("img") || target.querySelector("img");
    if (img && img.src && !/\/people\/|\/person\/|\/albums\//i.test(img.src)) {
      const m = img.src.match(/(?:assets?|asset\/thumbnail)\/([a-f0-9-]{36})/i);
      if (m) return m[1];
    }
    return null;
  }

  function injectAnchorFromTarget(target) {
    if (isSearchActive || /\/photos\/[a-f0-9-]{36}/i.test(window.location.pathname)) return;
    const assetId = extractAssetIdFromTarget(target);
    if (!assetId) return;

    lastActivePhotoId = assetId;
    try {
      const u = new URL(window.location.href);
      u.searchParams.set("at", assetId);
      history.replaceState(history.state, "", u.pathname + u.search + u.hash);
    } catch (_) {}
  }

  // 统一触控调度器：合并轻触锚点预埋与大图下拉关闭，避免多重事件监听
  let tStartX = 0;
  let tStartY = 0;
  let tTarget = null;
  let tMoved = false;
  let isViewerDrag = false;

  window.addEventListener(
    "touchstart",
    (e) => {
      if (e.touches.length !== 1) {
        tTarget = null;
        return;
      }
      tStartX = e.touches[0].clientX;
      tStartY = e.touches[0].clientY;
      tMoved = false;
      isViewerDrag = false;

      const isViewer = /\/photos\/[a-f0-9-]{36}/i.test(window.location.pathname);
      if (isViewer) {
        tTarget = null;
        if (!window.visualViewport || window.visualViewport.scale <= 1.05) {
          isViewerDrag = true;
        }
      } else if (!isSearchActive) {
        tTarget = e.target;
      }
    },
    { capture: true, passive: true }
  );

  window.addEventListener(
    "touchmove",
    (e) => {
      if (e.touches.length !== 1) return;
      const dx = e.touches[0].clientX - tStartX;
      const dy = e.touches[0].clientY - tStartY;

      if (isViewerDrag) {
        if (dy > 18 && dy > Math.abs(dx) * 1.5 && e.cancelable) {
          e.preventDefault();
        }
      } else if (tTarget && Math.hypot(dx, dy) > 10) {
        tMoved = true;
        tTarget = null;
      }
    },
    { capture: true, passive: false }
  );

  window.addEventListener(
    "touchend",
    (e) => {
      if (isViewerDrag) {
        isViewerDrag = false;
        const touch = e.changedTouches[0];
        if (touch && touch.clientY - tStartY > 65 && (touch.clientY - tStartY) > Math.abs(touch.clientX - tStartX) * 1.3) {
          history.back();
        }
        return;
      }
      if (!tMoved && tTarget) {
        const target = tTarget;
        tTarget = null;
        injectAnchorFromTarget(target);
      }
    },
    { capture: true, passive: true }
  );

  document.addEventListener("click", (e) => injectAnchorFromTarget(e.target), true);

  function fixImmichScrollLock() {
    setTimeout(() => {
      document.body.style.overflow = "";
      document.documentElement.style.overflow = "";
      const main = getMainElement();
      if (main) {
        main.style.overflow = "";
      }
    }, 100);
  }

  window.addEventListener("popstate", () => {
    if (expectingLightboxPop) {
      expectingLightboxPop = false;
      return;
    }
    if (isLightboxActive) {
      isLightboxActive = false;
      sendToIframe({ type: "IMMICH_CLOSE_LIGHTBOX" });
      return;
    }
    if (isSearchActive) {
      closeSearchView(true);
      return;
    }
    if (lastActivePhotoId && !/\/photos\/[a-f0-9-]{36}/i.test(window.location.pathname)) {
      try {
        const u = new URL(window.location.href);
        if (u.searchParams.get("at") !== lastActivePhotoId) {
          u.searchParams.set("at", lastActivePhotoId);
          history.replaceState(history.state, "", u.pathname + u.search + u.hash);
        }
      } catch (_) {}
    }
    fixImmichScrollLock();
  });

  document.addEventListener(
    "click",
    (e) => {
      const target = e.target;
      if (!target || !(target instanceof Element) || !isSearchActive) return;
      const logoOrHome = target.closest('a[href="/photos"], a[href="/"]');
      const menuBtn = target.closest("#top-menu-button, button[aria-label=\'主菜单\']");
      if (logoOrHome || menuBtn) {
        closeSearchView(false);
      }
    },
    true
  );
})();