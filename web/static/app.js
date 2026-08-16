(() => {
  const body = document.body;
  const isAdmin = String(body.dataset.isAdmin).toLowerCase() === 'true';
  const username = body.dataset.username || '';
  const view = body.dataset.view || 'library';
  const readerComicID = Number(body.dataset.readerComicId || 0);

  const LEGACY_SETTINGS_KEY_V5 = 'zipmanga_reader_settings_v5';
  const LEGACY_SETTINGS_KEY_V4 = 'zipmanga_reader_settings_v4';
  const LIBRARY_STATE_KEY = 'zipmanga_library_state_v1';
  const SINGLE_PAGE_READER = true;
  const DEFAULT_LIBRARY_PAGE_SIZE = 100;
  const LIBRARY_PAGE_SIZE_OPTIONS = [50, 100, 200, 500];
  const DEFAULT_RUNTIME_SETTINGS = {
    memoryLimitMB: 512,
    diskCacheMB: 10240,
    pagePrefetchPages: 10,
    pageLoadParallelism: 4,
    webpQuality: 75,
    webpMethod: 0,
    webpConcurrency: 2,
    webpOutputRoot: ''
  };
  const ENHANCE_MODE_OFF = 'off';
  const ENHANCE_MODE_LEVEL1 = 'level1';
  const ENHANCE_MODE_LEVEL2 = 'level2';
  const DEFAULT_ENHANCE_MODE = ENHANCE_MODE_LEVEL1;
  const READER_RENDER_REVISION = 'r8';
  const SCROLL_RENDER_MAX_CONCURRENT = 2;
  const ENHANCE_RENDER_PROFILES = Object.freeze({
    [ENHANCE_MODE_OFF]: {
      requestedWidthScale: 0.72,
      requestedWidthMax: 1280,
      dprMax: 1.0
    },
    [ENHANCE_MODE_LEVEL1]: {
      requestedWidthScale: 0.88,
      requestedWidthMax: 1920,
      dprMax: 1.2
    },
    [ENHANCE_MODE_LEVEL2]: {
      requestedWidthScale: 0.98,
      requestedWidthMax: 2560,
      dprMax: 1.35
    }
  });
  const PDF_COVER_MAX_CONCURRENT = 2;
  const PDF_COVER_CACHE_MAX = 120;
  const baseURL = new URL(document.baseURI);
  const apiURL = (relativePath) => new URL(relativePath.replace(/^\/+/, ''), baseURL).toString();
  const appURL = (relativePath) => {
    const value = String(relativePath || '').trim();
    if (!value || value === '/') return new URL('./', baseURL).toString();
    if (/^https?:\/\//i.test(value)) return value;
    return new URL(value.replace(/^\/+/, ''), baseURL).toString();
  };
  const pdfjsLib = window.pdfjsLib || null;
  if (pdfjsLib && pdfjsLib.GlobalWorkerOptions) {
    pdfjsLib.GlobalWorkerOptions.workerSrc = appURL('static/vendor/pdfjs/pdf.worker.min.js');
  }
  const DEFAULT_SETTINGS = {
    mode: 'paged',
    fit: 'width',
    enhance: DEFAULT_ENHANCE_MODE,
    sharpen: false,
    mobileCompact: true,
    spreadDirection: 'rtl'
  };

  const state = {
    mounts: [],
    rootDirectories: [],
    mountRootPaths: [],
    mountTreeExpanded: new Set(),
    comics: [],
    library: {
      page: 1,
      pageSize: DEFAULT_LIBRARY_PAGE_SIZE,
      total: 0,
      totalPages: 0
    },
    favorites: {
      comics: [],
      page: 1,
      pageSize: DEFAULT_LIBRARY_PAGE_SIZE,
      total: 0,
      totalPages: 0
    },
    runtime: { ...DEFAULT_RUNTIME_SETTINGS },
    archiveTask: {
      items: [],
      selectedID: '',
      lastSeq: 0,
      finalRenderedID: '',
      pollTimer: null
    },
    webpConvert: {
      runningTask: null,
      items: [],
      selectedTaskID: '',
      lastSeq: 0,
      finalRenderedID: '',
      pollTimer: null,
      treeExpanded: new Set()
    },
    nameFormatPreview: {
      id: '',
      total: 0,
      mountID: 0,
      truncated: false,
      sample: [],
      errors: []
    },
    imageCache: new Map(),
    imageObjectURLs: new Set(),
    pdfCoverObserver: null,
    pdfCoverQueue: [],
    pdfCoverInFlight: 0,
    pdfCoverTaskMap: new Map(),
    pdfCoverCache: new Map(),
    pdfCoverCacheOrder: [],
    scrollRenderQueue: [],
    scrollRenderPending: new Set(),
    scrollRenderInFlight: 0,
    scrollObserver: null,
    scrollRAF: 0,
    windowScrollBound: false,
    renderEpoch: 0,
    readerSlotRenderSeq: 0,
    reader: {
      comic: null,
      totalPages: 0,
      pageCountKnown: true,
      currentPage: 0,
      saveTimer: null,
      lastSavedPage: -1,
      settings: cloneDefaultSettings(),
      spread: 'single',
      renderedPages: new Set(),
      pseudoFullscreen: false,
      fullscreenHistoryPushed: false,
      pdfDoc: null,
      pdfLoadTask: null
    }
  };

  const el = {
    userBadge: document.getElementById('userBadge'),
    logoutBtn: document.getElementById('logoutBtn'),

    viewLibrary: document.getElementById('viewLibrary'),
    viewFavorites: document.getElementById('viewFavorites'),
    viewHistory: document.getElementById('viewHistory'),
    viewReader: document.getElementById('viewReader'),
    viewSettings: document.getElementById('viewSettings'),

    searchInput: document.getElementById('searchInput'),
    mountFilter: document.getElementById('mountFilter'),
    folderFilter: document.getElementById('folderFilter'),
    sortRule: document.getElementById('sortRule'),
    statusFilter: document.getElementById('statusFilter'),
    refreshBtn: document.getElementById('refreshBtn'),
    summaryText: document.getElementById('summaryText'),
    libraryPrevPageBtn: document.getElementById('libraryPrevPageBtn'),
    libraryNextPageBtn: document.getElementById('libraryNextPageBtn'),
    libraryPageText: document.getElementById('libraryPageText'),
    libraryPageSize: document.getElementById('libraryPageSize'),
    comicGrid: document.getElementById('comicGrid'),
    favoritesSummaryText: document.getElementById('favoritesSummaryText'),
    favoritesPrevPageBtn: document.getElementById('favoritesPrevPageBtn'),
    favoritesNextPageBtn: document.getElementById('favoritesNextPageBtn'),
    favoritesPageText: document.getElementById('favoritesPageText'),
    favoritesPageSize: document.getElementById('favoritesPageSize'),
    favoritesRefreshBtn: document.getElementById('favoritesRefreshBtn'),
    favoritesMountFilter: document.getElementById('favoritesMountFilter'),
    favoritesFolderFilter: document.getElementById('favoritesFolderFilter'),
    favoritesGrid: document.getElementById('favoritesGrid'),
    historyGrid: document.getElementById('historyGrid'),

    readerTopbar: document.getElementById('readerTopbar'),
    readerSide: document.getElementById('readerSide'),
    readerStage: document.getElementById('readerStage'),
    readerTitle: document.getElementById('readerTitle'),
    readerSubTitle: document.getElementById('readerSubTitle'),
    readerProgressText: document.getElementById('readerProgressText'),
    readerPages: document.getElementById('readerPages'),
    readerPagedWrap: document.getElementById('readerPagedWrap'),
    readerScrollWrap: document.getElementById('readerScrollWrap'),
    pagedSlotLeft: document.getElementById('pagedSlotLeft'),
    pagedSlotRight: document.getElementById('pagedSlotRight'),
    readerFavoriteBtn: document.getElementById('readerFavoriteBtn'),
    readerFullscreenBtn: document.getElementById('readerFullscreenBtn'),
    readerFirstBtn: document.getElementById('readerFirstBtn'),
    readerLastBtn: document.getElementById('readerLastBtn'),
    closeReaderBtn: document.getElementById('closeReaderBtn'),

    pageSlider: document.getElementById('pageSlider'),
    readerPrevBtn: document.getElementById('readerPrevBtn'),
    readerNextBtn: document.getElementById('readerNextBtn'),

    settingsForm: document.getElementById('settingsForm'),
    settingsMode: document.getElementById('settingsMode'),
    settingsFit: document.getElementById('settingsFit'),
    settingsEnhance: document.getElementById('settingsEnhance'),
    settingsSpreadDirection: document.getElementById('settingsSpreadDirection'),
    settingsMobileCompact: document.getElementById('settingsMobileCompact'),
    passwordForm: document.getElementById('passwordForm'),
    currentPassword: document.getElementById('currentPassword'),
    newPassword: document.getElementById('newPassword'),
    confirmPassword: document.getElementById('confirmPassword'),

    scanBtn: document.getElementById('scanBtn'),
    scanStatusText: document.getElementById('scanStatusText'),
    webpConvertForm: document.getElementById('webpConvertForm'),
    webpConvertSourceRoot: document.getElementById('webpConvertSourceRoot'),
    webpConvertSourcePath: document.getElementById('webpConvertSourcePath'),
    webpConvertBrowseRootBtn: document.getElementById('webpConvertBrowseRootBtn'),
    webpConvertBrowsePath: document.getElementById('webpConvertBrowsePath'),
    webpConvertTreeRoot: document.getElementById('webpConvertTreeRoot'),
    webpConvertOutputRoot: document.getElementById('webpConvertOutputRoot'),
    webpConvertQuality: document.getElementById('webpConvertQuality'),
    webpConvertMethod: document.getElementById('webpConvertMethod'),
    webpConvertConcurrency: document.getElementById('webpConvertConcurrency'),
    webpConvertSaveDefaultsBtn: document.getElementById('webpConvertSaveDefaultsBtn'),
    webpConvertRunBtn: document.getElementById('webpConvertRunBtn'),
    webpConvertTaskRefreshBtn: document.getElementById('webpConvertTaskRefreshBtn'),
    webpConvertCancelBtn: document.getElementById('webpConvertCancelBtn'),
    webpConvertTaskList: document.getElementById('webpConvertTaskList'),
    webpConvertProgressText: document.getElementById('webpConvertProgressText'),
    webpConvertProgressFill: document.getElementById('webpConvertProgressFill'),
    webpConvertTaskMeta: document.getElementById('webpConvertTaskMeta'),
    webpConvertTaskSummary: document.getElementById('webpConvertTaskSummary'),
    archiveWorkflowForm: document.getElementById('archiveWorkflowForm'),
    archiveWorkflowMount: document.getElementById('archiveWorkflowMount'),
    archiveWorkflowPassword: document.getElementById('archiveWorkflowPassword'),
    archiveCollectBtn: document.getElementById('archiveCollectBtn'),
    archiveExtractBtn: document.getElementById('archiveExtractBtn'),
    archiveOrganizeBtn: document.getElementById('archiveOrganizeBtn'),
    archiveRepackBtn: document.getElementById('archiveRepackBtn'),
    nameFormatPreviewBtn: document.getElementById('nameFormatPreviewBtn'),
    nameFormatApplyBtn: document.getElementById('nameFormatApplyBtn'),
    nameFormatPreviewSummary: document.getElementById('nameFormatPreviewSummary'),
    nameFormatDiffList: document.getElementById('nameFormatDiffList'),
    archiveTaskRefreshBtn: document.getElementById('archiveTaskRefreshBtn'),
    archiveTaskList: document.getElementById('archiveTaskList'),
    archiveWorkflowOutput: document.getElementById('archiveWorkflowOutput'),
    runtimeForm: document.getElementById('runtimeForm'),
    runtimeMemoryLimitMB: document.getElementById('runtimeMemoryLimitMB'),
    runtimeDiskCacheMB: document.getElementById('runtimeDiskCacheMB'),
    runtimePagePrefetchPages: document.getElementById('runtimePagePrefetchPages'),
    runtimePageLoadParallelism: document.getElementById('runtimePageLoadParallelism'),
    mountForm: document.getElementById('mountForm'),
    mountType: document.getElementById('mountType'),
    mountList: document.getElementById('mountList'),
    rootDirectoryForm: document.getElementById('rootDirectoryForm'),
    rootDirectoryList: document.getElementById('rootDirectoryList'),
    mountRootDirectorySelect: document.getElementById('mountRootDirectorySelect'),
    mountRootPaths: document.getElementById('mountRootPaths'),
    mountRootPathsEmpty: document.getElementById('mountRootPathsEmpty'),
    mountBrowsePathInput: document.getElementById('mountBrowsePathInput'),
    mountBrowseRootBtn: document.getElementById('mountBrowseRootBtn'),
    mountTreeRoot: document.getElementById('mountTreeRoot'),
    userForm: document.getElementById('userForm'),
    userList: document.getElementById('userList'),
    settingsIndexButtons: document.querySelectorAll('.settings-index-btn'),
    settingsModules: document.querySelectorAll('.settings-module')
  };

  let searchTimer = null;
  let scanTimer = null;
  let swipeState = null;
  let resizeTimer = null;
  let libraryScrollTimer = null;
  let libraryPendingScrollY = null;
  let lastScanRunning = false;
  let lastScannedCount = 0;
  let settingsSaveTimer = null;

  function applyPendingSelectValue(selectEl, fallbackValue) {
    if (!selectEl) return;
    const pending = typeof selectEl.dataset.pendingValue === 'string' ? selectEl.dataset.pendingValue : '';
    const target = pending || String(fallbackValue || '');
    if (target && [...selectEl.options].some(o => o.value === target)) {
      selectEl.value = target;
    }
    delete selectEl.dataset.pendingValue;
  }

  function cloneDefaultSettings() {
    return { ...DEFAULT_SETTINGS };
  }

  function normalizeEnhanceMode(value) {
    const raw = String(value || '').trim();
    if (!raw) return DEFAULT_ENHANCE_MODE;
    if (raw === ENHANCE_MODE_OFF || raw === 'close' || raw === 'disabled') return ENHANCE_MODE_OFF;
    if (raw === ENHANCE_MODE_LEVEL1 || raw === 'balanced' || raw === 'clarity' || raw === 'strong') return ENHANCE_MODE_LEVEL1;
    if (raw === ENHANCE_MODE_LEVEL2 || raw === 'quality' || raw === 'ultra' || raw === 'ai-2x' || raw === 'ai-4x') return ENHANCE_MODE_LEVEL2;
    return DEFAULT_ENHANCE_MODE;
  }

  function enhanceRenderProfile(enhance) {
    return ENHANCE_RENDER_PROFILES[normalizeEnhanceMode(enhance)] || ENHANCE_RENDER_PROFILES[DEFAULT_ENHANCE_MODE];
  }

  function getNetworkScaleFactor() {
    const conn = navigator.connection || navigator.mozConnection || navigator.webkitConnection || null;
    if (!conn) return 1;
    if (conn.saveData) return 0.72;
    const et = String(conn.effectiveType || '').toLowerCase();
    if (et === 'slow-2g') return 0.58;
    if (et === '2g') return 0.68;
    if (et === '3g') return 0.84;
    return 1;
  }

  function nextReaderRenderEpoch() {
    state.renderEpoch += 1;
    return state.renderEpoch;
  }

  function shouldSharpenByEnhance() {
    return false;
  }

  function isRTL() {
    return state.reader.settings.spreadDirection === 'rtl';
  }

  function isPDFComic() {
    return String(state.reader.comic?.format || '').toLowerCase() === 'pdf';
  }

  function getEnhanceModeByFormat() {
    return normalizeEnhanceMode(state.reader.settings?.enhance || DEFAULT_ENHANCE_MODE);
  }

  function getCurrentEnhanceMode() {
    return getEnhanceModeByFormat();
  }

  function imageCacheKey(index, requestWidth = computeRequestedWidth()) {
    const enhance = getCurrentEnhanceMode();
    const formatKey = isPDFComic() ? 'pdf' : 'img';
    return `${formatKey}:${READER_RENDER_REVISION}:${index}:${enhance}:w${requestWidth}`;
  }

  function canRenderPDFCoverPreviews() {
    return !!(pdfjsLib && typeof pdfjsLib.getDocument === 'function');
  }

  function cachePDFCoverURL(key, url) {
    if (!key || !url) return;
    if (state.pdfCoverCache.has(key)) {
      const old = state.pdfCoverCache.get(key);
      if (old && old !== url) URL.revokeObjectURL(old);
      state.pdfCoverCache.set(key, url);
      return;
    }
    state.pdfCoverCache.set(key, url);
    state.pdfCoverCacheOrder.push(key);
    while (state.pdfCoverCacheOrder.length > PDF_COVER_CACHE_MAX) {
      const oldest = state.pdfCoverCacheOrder.shift();
      if (!oldest) break;
      const oldURL = state.pdfCoverCache.get(oldest);
      if (oldURL) URL.revokeObjectURL(oldURL);
      state.pdfCoverCache.delete(oldest);
    }
  }

  function trackImageObjectURL(url) {
    if (!url) return;
    state.imageObjectURLs.add(url);
  }

  function clearReaderImageCache() {
    state.imageCache.clear();
    state.imageObjectURLs.forEach((url) => {
      if (url) URL.revokeObjectURL(url);
    });
    state.imageObjectURLs.clear();
  }

  function resetScrollRenderQueue() {
    state.scrollRenderQueue = [];
    state.scrollRenderPending.clear();
    state.scrollRenderInFlight = 0;
  }

  async function loadImageFromURL(src) {
    return new Promise((resolve, reject) => {
      const image = new Image();
      image.decoding = 'async';
      image.onload = () => resolve(image);
      image.onerror = () => reject(new Error('image load failed'));
      image.src = src;
    });
  }

  async function fetchReaderImage(url) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 15000);
    let resp = null;
    try {
      resp = await fetch(url, { credentials: 'same-origin', signal: controller.signal });
    } finally {
      clearTimeout(timer);
    }
    if (resp.status === 401) {
      location.href = appURL('login');
      throw new Error('unauthorized');
    }
    if (!resp.ok) {
      throw new Error(`request failed: ${resp.status}`);
    }
    const contentType = String(resp.headers.get('content-type') || '').toLowerCase();
    if (!contentType.startsWith('image/')) {
      throw new Error(`unexpected content type: ${contentType}`);
    }
    const isFallback = resp.headers.get('x-zm-page-fallback') === '1';
    const blob = await resp.blob();
    if (!blob || blob.size <= 0) {
      throw new Error('empty image blob');
    }
    const objectURL = URL.createObjectURL(blob);
    try {
      const img = await loadImageFromURL(objectURL);
      trackImageObjectURL(objectURL);
      img.dataset.pageFallback = isFallback ? '1' : '0';
      return img;
    } catch (err) {
      URL.revokeObjectURL(objectURL);
      throw err;
    }
  }

  async function buildPDFCoverObjectURL(comicID) {
    const id = Number(comicID || 0);
    if (!id) return '';
    if (!canRenderPDFCoverPreviews()) return '';

    const loadingTask = pdfjsLib.getDocument({
      url: apiURL(`api/comics/${id}/file`),
      withCredentials: true,
      disableAutoFetch: true,
      disableStream: true,
      rangeChunkSize: 256 * 1024
    });
    const pdfDoc = await loadingTask.promise;
    let page = null;
    try {
      if (!pdfDoc || Number(pdfDoc.numPages || 0) <= 0) return '';
      page = await pdfDoc.getPage(1);
      const viewport = page.getViewport({ scale: 1 });
      const targetWidth = 320;
      const baseWidth = Math.max(1, Number(viewport.width || 1));
      const scale = Math.max(0.35, Math.min(2.5, targetWidth / baseWidth));
      const renderViewport = page.getViewport({ scale });

      const canvas = document.createElement('canvas');
      canvas.width = Math.max(1, Math.round(renderViewport.width));
      canvas.height = Math.max(1, Math.round(renderViewport.height));
      const ctx = canvas.getContext('2d', { alpha: false });
      if (!ctx) return '';
      await page.render({ canvasContext: ctx, viewport: renderViewport }).promise;
      const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.88));
      if (!blob) return '';
      return URL.createObjectURL(blob);
    } finally {
      if (page) page.cleanup();
      await pdfDoc.destroy().catch(() => {});
    }
  }

  function pumpPDFCoverQueue() {
    while (state.pdfCoverInFlight < PDF_COVER_MAX_CONCURRENT && state.pdfCoverQueue.length > 0) {
      const task = state.pdfCoverQueue.shift();
      if (!task) break;
      state.pdfCoverInFlight += 1;

      buildPDFCoverObjectURL(task.comicID)
        .then((url) => {
          if (url) cachePDFCoverURL(task.key, url);
          task.targets.forEach((img) => {
            if (!img || !img.isConnected) return;
            if (img.dataset.coverCacheKey !== task.key) return;
            if (url) img.src = url;
          });
        })
        .catch(() => {})
        .finally(() => {
          state.pdfCoverInFlight = Math.max(0, state.pdfCoverInFlight - 1);
          state.pdfCoverTaskMap.delete(task.key);
          pumpPDFCoverQueue();
        });
    }
  }

  function enqueuePDFCover(img) {
    if (!img) return;
    const key = String(img.dataset.coverCacheKey || '').trim();
    const comicID = Number(img.dataset.comicId || 0);
    if (!key || !comicID) return;

    const cachedURL = state.pdfCoverCache.get(key);
    if (cachedURL) {
      img.src = cachedURL;
      return;
    }

    const existing = state.pdfCoverTaskMap.get(key);
    if (existing) {
      existing.targets.add(img);
      return;
    }

    const task = {
      key,
      comicID,
      targets: new Set([img])
    };
    state.pdfCoverTaskMap.set(key, task);
    state.pdfCoverQueue.push(task);
    pumpPDFCoverQueue();
  }

  function ensurePDFCoverObserver() {
    if (state.pdfCoverObserver || !canRenderPDFCoverPreviews()) return;
    state.pdfCoverObserver = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) return;
        const img = entry.target;
        state.pdfCoverObserver.unobserve(img);
        enqueuePDFCover(img);
      });
    }, { root: null, rootMargin: '800px 0px 800px 0px' });
  }

  function observePDFCoverImage(img) {
    if (!img || !canRenderPDFCoverPreviews()) return;
    const key = String(img.dataset.coverCacheKey || '');
    if (!key) return;
    const cachedURL = state.pdfCoverCache.get(key);
    if (cachedURL) {
      img.src = cachedURL;
      return;
    }
    ensurePDFCoverObserver();
    if (state.pdfCoverObserver) state.pdfCoverObserver.observe(img);
  }

  async function destroyReaderPDF() {}

  function normalizeSettingsPayload(input) {
    const v = input || {};
    const legacyEnhanceRaw = String(v.enhance || v.enhanceMode || '').trim();
    const legacyEnhance = legacyEnhanceRaw ? normalizeEnhanceMode(legacyEnhanceRaw) : '';
    const enhanceZip = normalizeEnhanceMode(v.enhance_zip || v.enhanceZip || DEFAULT_ENHANCE_MODE);
    const enhancePDF = normalizeEnhanceMode(v.enhance_pdf || v.enhancePDF || DEFAULT_ENHANCE_MODE);
    let enhance = legacyEnhance;
    if (!enhance) {
      if (enhanceZip === ENHANCE_MODE_LEVEL2 || enhancePDF === ENHANCE_MODE_LEVEL2) {
        enhance = ENHANCE_MODE_LEVEL2;
      } else if (enhanceZip === ENHANCE_MODE_LEVEL1 || enhancePDF === ENHANCE_MODE_LEVEL1) {
        enhance = ENHANCE_MODE_LEVEL1;
      } else {
        enhance = ENHANCE_MODE_OFF;
      }
    }
    enhance = normalizeEnhanceMode(enhance);
    return {
      mode: v.mode === 'scroll' ? 'scroll' : 'paged',
      fit: v.fit === 'height' ? 'height' : 'width',
      enhance,
      sharpen: shouldSharpenByEnhance(enhance),
      mobileCompact: typeof v.mobile_compact === 'boolean'
        ? v.mobile_compact
        : (typeof v.mobileCompact === 'boolean' ? v.mobileCompact : true),
      spreadDirection: v.spread_direction === 'ltr' || v.spreadDirection === 'ltr' ? 'ltr' : 'rtl'
    };
  }

  function readLegacyLocalSettings() {
    try {
      const raw = localStorage.getItem(LEGACY_SETTINGS_KEY_V5) || localStorage.getItem(LEGACY_SETTINGS_KEY_V4);
      if (!raw) return null;
      return normalizeSettingsPayload(JSON.parse(raw));
    } catch (_) {
      return null;
    }
  }

  function clearLegacyLocalSettings() {
    try {
      localStorage.removeItem(LEGACY_SETTINGS_KEY_V5);
      localStorage.removeItem(LEGACY_SETTINGS_KEY_V4);
    } catch (_) {}
  }

  function buildSettingsRequestBody() {
    const enhance = normalizeEnhanceMode(state.reader.settings.enhance);
    return {
      mode: state.reader.settings.mode,
      fit: state.reader.settings.fit,
      enhance_zip: enhance,
      enhance_pdf: enhance,
      spread_direction: state.reader.settings.spreadDirection === 'ltr' ? 'ltr' : 'rtl',
      mobile_compact: !!state.reader.settings.mobileCompact
    };
  }

  async function saveSettingsNow() {
    const data = await fetchJSON('api/me/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(buildSettingsRequestBody())
    });
    if (data?.settings) {
      state.reader.settings = normalizeSettingsPayload(data.settings);
    }
  }

  function saveSettings() {
    if (settingsSaveTimer) clearTimeout(settingsSaveTimer);
    settingsSaveTimer = setTimeout(() => {
      settingsSaveTimer = null;
      saveSettingsNow().catch(() => {});
    }, 160);
  }

  async function loadSettingsFromServer() {
    const data = await fetchJSON('api/me/settings');
    if (data?.settings) {
      state.reader.settings = normalizeSettingsPayload(data.settings);
    } else {
      state.reader.settings = cloneDefaultSettings();
    }

    const hasCustom = !!data?.has_custom;
    const legacy = readLegacyLocalSettings();
    if (!hasCustom && legacy) {
      state.reader.settings = normalizeSettingsPayload(legacy);
      try {
        await saveSettingsNow();
      } catch (_) {
        return;
      }
    }
    clearLegacyLocalSettings();
  }

  function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
  }

  async function fetchJSON(url, options) {
    const opts = options ? { ...options } : {};
    const method = String(opts.method || 'GET').toUpperCase();
    const headers = { Accept: 'application/json', ...(opts.headers || {}) };
    const retries = method === 'GET' ? 1 : 0;
    let lastErr = null;

    for (let attempt = 0; attempt <= retries; attempt += 1) {
      try {
        const resp = await fetch(appURL(url), { ...opts, headers });

        if (resp.status === 401) {
          location.href = appURL('login');
          return null;
        }

        const text = await resp.text();
        let data = {};
        if (text) {
          try {
            data = JSON.parse(text);
          } catch (_) {
            data = {};
          }
        }

        if (!resp.ok) {
          const message = typeof data.error === 'string' && data.error ? data.error : `${resp.status} ${resp.statusText || '请求失败'}`;
          if (attempt < retries && resp.status >= 500) {
            await sleep(200 * (attempt + 1));
            continue;
          }
          const err = new Error(message);
          err.status = resp.status;
          throw err;
        }
        return data;
      } catch (err) {
        lastErr = err;
        if (attempt < retries) {
          await sleep(220 * (attempt + 1));
          continue;
        }
      }
    }

    throw lastErr || new Error('请求失败');
  }

  function showView(target) {
    const map = {
      library: el.viewLibrary,
      favorites: el.viewFavorites,
      history: el.viewHistory,
      reader: el.viewReader,
      settings: el.viewSettings
    };
    Object.entries(map).forEach(([k, node]) => {
      if (node) node.hidden = k !== target;
    });
    document.querySelectorAll('.tab-link').forEach(a => a.classList.toggle('active', a.dataset.tab === target));
    document.body.classList.toggle('mobile-compact', target === 'reader' && state.reader.settings.mobileCompact);
    document.body.classList.toggle('reader-active', target === 'reader');
  }

  function mountTypeName(v) {
    if (v === 'local') return '本地';
    if (v === 'smb') return 'SMB';
    if (v === 'webdav') return 'WebDAV';
    return v;
  }

  function localMountsForArchiveWorkflow() {
    return (state.mounts || []).filter((item) => item && item.type === 'local');
  }

  function renderArchiveWorkflowMountOptions() {
    if (!el.archiveWorkflowMount) return;
    const previous = String(el.archiveWorkflowMount.value || '');
    el.archiveWorkflowMount.innerHTML = '';

    const localMounts = localMountsForArchiveWorkflow();
    if (!localMounts.length) {
      const opt = document.createElement('option');
      opt.value = '';
      opt.textContent = '暂无 local 挂载';
      el.archiveWorkflowMount.appendChild(opt);
      return;
    }

    localMounts.forEach((mount) => {
      const opt = document.createElement('option');
      opt.value = String(mount.id);
      const status = mount.enabled ? '启用' : '停用';
      opt.textContent = `${mount.name} (#${mount.id}, ${status})`;
      el.archiveWorkflowMount.appendChild(opt);
    });

    const hasPrevious = localMounts.some((mount) => String(mount.id) === previous);
    const nextValue = hasPrevious ? previous : String(localMounts[0].id);
    el.archiveWorkflowMount.value = nextValue;
    if (previous && previous !== nextValue) {
      resetNameFormatPreviewState();
    }
  }

  function renderWebPSourceRootOptions() {
    if (!el.webpConvertSourceRoot) return;
    const previous = String(el.webpConvertSourceRoot.value || '');
    el.webpConvertSourceRoot.innerHTML = '';
    if (!Array.isArray(state.rootDirectories) || state.rootDirectories.length === 0) {
      const opt = document.createElement('option');
      opt.value = '';
      opt.textContent = '请先配置根目录库';
      el.webpConvertSourceRoot.appendChild(opt);
      return;
    }
    state.rootDirectories.forEach((item) => {
      const opt = document.createElement('option');
      opt.value = String(item.id);
      opt.textContent = `${item.name} · ${normalizeRootPath(item.path || '/')}`;
      el.webpConvertSourceRoot.appendChild(opt);
    });
    const hasPrevious = state.rootDirectories.some((item) => String(item.id) === previous);
    el.webpConvertSourceRoot.value = hasPrevious ? previous : String(state.rootDirectories[0].id);
  }

  function renderWebPOutputRootOptions() {
    if (!el.webpConvertOutputRoot) return;
    const previousPath = String(state.runtime.webpOutputRoot || '').trim();
    const previousValue = String(el.webpConvertOutputRoot.value || '');
    el.webpConvertOutputRoot.innerHTML = '';
    if (!Array.isArray(state.rootDirectories) || state.rootDirectories.length === 0) {
      const opt = document.createElement('option');
      opt.value = '';
      opt.textContent = '请先配置根目录库';
      el.webpConvertOutputRoot.appendChild(opt);
      return;
    }
    state.rootDirectories.forEach((item) => {
      const opt = document.createElement('option');
      opt.value = String(item.id);
      opt.textContent = `${item.name} · ${normalizeRootPath(item.path || '/')}`;
      el.webpConvertOutputRoot.appendChild(opt);
    });
    const matchedByPath = state.rootDirectories.find((item) => String(item.path || '').trim() === previousPath);
    if (matchedByPath) {
      el.webpConvertOutputRoot.value = String(matchedByPath.id);
      return;
    }
    const hasPreviousValue = state.rootDirectories.some((item) => String(item.id) === previousValue);
    el.webpConvertOutputRoot.value = hasPreviousValue ? previousValue : String(state.rootDirectories[0].id);
  }

  function setArchiveWorkflowButtonsDisabled(disabled) {
    [el.archiveCollectBtn, el.archiveExtractBtn, el.archiveOrganizeBtn, el.archiveRepackBtn].forEach((btn) => {
      if (btn) btn.disabled = !!disabled;
    });
  }

  function appendArchiveWorkflowOutput(content) {
    if (!el.archiveWorkflowOutput) return;
    const text = String(content || '').trim();
    if (!text) return;
    const previous = String(el.archiveWorkflowOutput.textContent || '').trim();
    el.archiveWorkflowOutput.textContent = previous ? `${previous}\n\n${text}` : text;
    el.archiveWorkflowOutput.scrollTop = el.archiveWorkflowOutput.scrollHeight;
  }

  function clearArchiveWorkflowOutput(text) {
    if (!el.archiveWorkflowOutput) return;
    el.archiveWorkflowOutput.textContent = String(text || '').trim();
    el.archiveWorkflowOutput.scrollTop = 0;
  }

  function archiveActionText(action) {
    const key = String(action || '').toLowerCase();
    if (key === 'collect') return '收集';
    if (key === 'extract') return '解压';
    if (key === 'organize') return '整理';
    if (key === 'repack') return '再压缩';
    if (key === 'webp_convert') return 'WebP 转换';
    if (key === 'format_names') return '格式化命名';
    return key || '-';
  }

  function archiveTaskStatusText(status) {
    const key = String(status || '').toLowerCase();
    if (key === 'queued') return '排队中';
    if (key === 'running') return '执行中';
    if (key === 'succeeded') return '已完成';
    if (key === 'failed') return '失败';
    if (key === 'canceled' || key === 'cancelled') return '已取消';
    return key || '未知';
  }

  function formatArchiveTaskLogLine(line) {
    const ts = line?.created_at ? new Date(line.created_at).toLocaleString() : new Date().toLocaleString();
    const level = String(line?.level || 'info').toUpperCase();
    return `[${ts}] [${level}] ${String(line?.message || '')}`;
  }

  function formatDateTime(value) {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '-';
    return date.toLocaleString();
  }

  function renderArchiveTaskList() {
    if (!el.archiveTaskList) return;
    el.archiveTaskList.innerHTML = '';
    if (!Array.isArray(state.archiveTask.items) || state.archiveTask.items.length === 0) {
      el.archiveTaskList.innerHTML = '<li><span class="hint">暂无后台任务</span></li>';
      return;
    }
    const frag = document.createDocumentFragment();
    state.archiveTask.items.forEach((item) => {
      const li = document.createElement('li');
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = `archive-task-item${String(item.id || '') === String(state.archiveTask.selectedID || '') ? ' active' : ''}`;
      const action = archiveActionText(item.action);
      const status = archiveTaskStatusText(item.status);
      const mountName = String(item.mount_name || '-');
      const createdAt = item.created_at ? new Date(item.created_at).toLocaleString() : '-';
      const summary = item.summary || {};
      const failed = Number(summary.failed || 0);
      btn.innerHTML = `
        <span class="archive-task-title">${action} · ${status}</span>
        <span class="archive-task-line">任务ID: ${String(item.id || '-')}</span>
        <span class="archive-task-line">挂载: ${mountName} (#${String(item.mount_id || '-')})</span>
        <span class="archive-task-line">创建: ${createdAt}</span>
        <span class="archive-task-line">失败: ${failed}</span>
      `;
      btn.addEventListener('click', () => {
        void selectArchiveTask(item.id, true);
      });
      li.appendChild(btn);
      frag.appendChild(li);
    });
    el.archiveTaskList.appendChild(frag);
  }

  async function loadArchiveTasks() {
    if (!isAdmin) return;
    const data = await fetchJSON('api/admin/archive-workflow/tasks?limit=40');
    state.archiveTask.items = Array.isArray(data?.items) ? data.items : [];
    renderArchiveTaskList();

    const selectedID = String(state.archiveTask.selectedID || '');
    if (!selectedID && state.archiveTask.items.length > 0) {
      await selectArchiveTask(state.archiveTask.items[0].id, true);
    }
    if (selectedID) {
      const found = state.archiveTask.items.some((item) => String(item.id) === selectedID);
      if (!found) {
        state.archiveTask.selectedID = '';
        state.archiveTask.lastSeq = 0;
        state.archiveTask.finalRenderedID = '';
        clearArchiveWorkflowOutput('当前任务已不存在，可能已被清理。');
      }
    }
  }

  function appendArchiveTaskFinalResult(task) {
    if (!task || String(task.id || '') === '') return;
    const summary = task.summary || {};
    appendArchiveWorkflowOutput(`任务结束: ${archiveTaskStatusText(task.status)}\n统计: 扫描 ${Number(summary.scanned || 0)} · 匹配 ${Number(summary.matched || 0)} · 移动 ${Number(summary.moved || 0)} · 解压 ${Number(summary.extracted || 0)} · 保留 ${Number(summary.kept || 0)} · 删除 ${Number(summary.deleted || 0)} · 压缩 ${Number(summary.zipped || 0)} · 失败 ${Number(summary.failed || 0)}`);
    const roots = Array.isArray(task.roots) ? task.roots : [];
    roots.forEach((root) => {
      const errors = Array.isArray(root?.errors) ? root.errors : [];
      if (!errors.length) return;
      appendArchiveWorkflowOutput(`失败明细(root=${String(root.root_path || '-')})`);
      errors.forEach((errText) => appendArchiveWorkflowOutput(String(errText)));
    });
  }

  async function loadArchiveTaskDetail(forceResetLog) {
    if (!isAdmin) return;
    const taskID = String(state.archiveTask.selectedID || '').trim();
    if (!taskID) return;
    const afterSeq = forceResetLog ? 0 : Number(state.archiveTask.lastSeq || 0);
    const data = await fetchJSON(`api/admin/archive-workflow/tasks/${encodeURIComponent(taskID)}?after_seq=${afterSeq}`);
    const task = data?.task || null;
    const logs = Array.isArray(data?.logs) ? data.logs : [];
    if (!task) return;

    if (forceResetLog) {
      state.archiveTask.lastSeq = 0;
      state.archiveTask.finalRenderedID = '';
      clearArchiveWorkflowOutput(`任务ID: ${String(task.id || '-')}\n动作: ${archiveActionText(task.action)}\n挂载: ${String(task.mount_name || '-')} (#${String(task.mount_id || '-')})\n状态: ${archiveTaskStatusText(task.status)}`);
    }
    if (logs.length > 0) {
      logs.forEach((line) => {
        appendArchiveWorkflowOutput(formatArchiveTaskLogLine(line));
        state.archiveTask.lastSeq = Math.max(state.archiveTask.lastSeq, Number(line.seq || 0));
      });
    } else if (afterSeq === 0) {
      appendArchiveWorkflowOutput('[日志为空]');
    }

    const status = String(task.status || '').toLowerCase();
    if ((status === 'succeeded' || status === 'failed') && state.archiveTask.finalRenderedID !== String(task.id || '')) {
      appendArchiveTaskFinalResult(task);
      state.archiveTask.finalRenderedID = String(task.id || '');
    }
  }

  async function selectArchiveTask(taskID, forceResetLog) {
    const nextID = String(taskID || '').trim();
    if (!nextID) return;
    if (String(state.archiveTask.selectedID || '') !== nextID) {
      state.archiveTask.lastSeq = 0;
      state.archiveTask.finalRenderedID = '';
    }
    state.archiveTask.selectedID = nextID;
    renderArchiveTaskList();
    await loadArchiveTaskDetail(!!forceResetLog);
  }

  function hasActiveArchiveTask() {
    return (state.archiveTask.items || []).some((item) => {
      const st = String(item?.status || '').toLowerCase();
      return st === 'queued' || st === 'running';
    });
  }

  function resetNameFormatPreviewState() {
    state.nameFormatPreview = {
      id: '',
      total: 0,
      mountID: 0,
      truncated: false,
      sample: [],
      errors: []
    };
    if (el.nameFormatApplyBtn) el.nameFormatApplyBtn.disabled = true;
    if (el.nameFormatPreviewSummary) {
      el.nameFormatPreviewSummary.textContent = '先点击“预览”，查看将被改名的文件/目录差异，再确认执行。';
    }
    if (el.nameFormatDiffList) {
      el.nameFormatDiffList.innerHTML = '';
    }
  }

  function renderNameFormatDiffList() {
    if (!el.nameFormatDiffList) return;
    el.nameFormatDiffList.innerHTML = '';
    const sample = Array.isArray(state.nameFormatPreview.sample) ? state.nameFormatPreview.sample : [];
    if (!sample.length) {
      el.nameFormatDiffList.innerHTML = '<li><span class="hint">无命名差异</span></li>';
      return;
    }
    const frag = document.createDocumentFragment();
    sample.forEach((item) => {
      const li = document.createElement('li');
      const oldLine = document.createElement('div');
      oldLine.className = 'diff-old';
      oldLine.textContent = `旧: ${String(item.old_path || '')}`;
      const newLine = document.createElement('div');
      newLine.className = 'diff-new';
      newLine.textContent = `新: ${String(item.new_path || '')}`;
      li.appendChild(oldLine);
      li.appendChild(newLine);
      frag.appendChild(li);
    });
    el.nameFormatDiffList.appendChild(frag);
  }

  async function previewNameFormat() {
    if (!isAdmin) return;
    const mountID = Number(el.archiveWorkflowMount?.value || 0);
    if (!Number.isFinite(mountID) || mountID <= 0) {
      alert('请先选择一个 local 挂载');
      return;
    }

    if (el.nameFormatPreviewBtn) el.nameFormatPreviewBtn.disabled = true;
    if (el.nameFormatApplyBtn) el.nameFormatApplyBtn.disabled = true;
    if (el.nameFormatPreviewSummary) {
      el.nameFormatPreviewSummary.textContent = '正在生成命名差异预览...';
    }
    if (el.nameFormatDiffList) {
      el.nameFormatDiffList.innerHTML = '';
    }

    try {
      const data = await fetchJSON('api/admin/archive-workflow/name-format/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mount_id: mountID,
          sample_limit: 300
        })
      });
      const total = Number(data?.total_changes || 0);
      const previewID = String(data?.preview_id || '');
      const errors = Array.isArray(data?.errors) ? data.errors.map(x => String(x || '')) : [];
      state.nameFormatPreview = {
        id: previewID,
        total,
        mountID,
        truncated: !!data?.truncated,
        sample: Array.isArray(data?.sample_items) ? data.sample_items : [],
        errors
      };
      renderNameFormatDiffList();
      if (el.nameFormatPreviewSummary) {
        const base = total > 0
          ? `检测到 ${total} 个命名差异${state.nameFormatPreview.truncated ? '（仅展示前 300 条）' : ''}。`
          : '未检测到命名差异。';
        const errText = errors.length > 0 ? ` 预览告警 ${errors.length} 条。` : '';
        el.nameFormatPreviewSummary.textContent = `${base}${errText}`;
      }
      if (el.nameFormatApplyBtn) {
        el.nameFormatApplyBtn.disabled = !(previewID && total > 0);
      }
    } catch (err) {
      resetNameFormatPreviewState();
      if (el.nameFormatPreviewSummary) {
        el.nameFormatPreviewSummary.textContent = `预览失败: ${err?.message || String(err)}`;
      }
      alert('预览失败: ' + (err?.message || String(err)));
    } finally {
      if (el.nameFormatPreviewBtn) el.nameFormatPreviewBtn.disabled = false;
    }
  }

  async function applyNameFormat() {
    if (!isAdmin) return;
    const previewID = String(state.nameFormatPreview.id || '').trim();
    const total = Number(state.nameFormatPreview.total || 0);
    const mountID = Number(el.archiveWorkflowMount?.value || 0);
    if (!previewID || total <= 0) {
      alert('请先执行预览，并确保存在命名差异');
      return;
    }
    if (Number(state.nameFormatPreview.mountID || 0) !== mountID) {
      alert('挂载已变更，请重新预览后再确认');
      return;
    }
    if (!confirm(`确认执行 UTF-8 命名格式化？将处理 ${total} 个差异项。`)) {
      return;
    }

    if (el.nameFormatApplyBtn) el.nameFormatApplyBtn.disabled = true;
    try {
      const data = await fetchJSON('api/admin/archive-workflow/name-format/apply', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ preview_id: previewID })
      });
      const task = data?.task || null;
      if (!task?.id) {
        throw new Error('后台任务创建失败：未返回任务ID');
      }
      appendArchiveWorkflowOutput(`[${new Date().toLocaleString()}] 已创建“格式化命名”后台任务: ${task.id}`);
      await loadArchiveTasks();
      await selectArchiveTask(task.id, true);
      ensureArchiveTaskPolling();
      resetNameFormatPreviewState();
    } catch (err) {
      alert('执行失败: ' + (err?.message || String(err)));
      if (el.nameFormatApplyBtn) el.nameFormatApplyBtn.disabled = false;
    }
  }

  function ensureArchiveTaskPolling() {
    if (!isAdmin) return;
    if (state.archiveTask.pollTimer) return;
    state.archiveTask.pollTimer = setInterval(async () => {
      try {
        await loadArchiveTasks();
        if (state.archiveTask.selectedID) {
          await loadArchiveTaskDetail(false);
        }
        if (!hasActiveArchiveTask() && state.archiveTask.pollTimer) {
          clearInterval(state.archiveTask.pollTimer);
          state.archiveTask.pollTimer = null;
        }
      } catch (_) {}
    }, 1800);
  }

  function renderWebPTaskSummary(task) {
    if (!el.webpConvertTaskSummary) return;
    if (!task) {
      el.webpConvertTaskSummary.innerHTML = '<p class="hint">选择任务后显示摘要信息。</p>';
      return;
    }
    const createdAt = formatDateTime(task.created_at);
    const startedAt = formatDateTime(task.started_at);
    const finishedAt = formatDateTime(task.finished_at);
    const errorText = String(task.error || '').trim();
    const sourcePath = String(task.source_path || '-');
    const outputDir = String(task.output_dir || '-');
    const currentArchive = String(task.current_archive || '').trim();
    el.webpConvertTaskSummary.innerHTML = `
      <dl class="task-summary-grid">
        <div><dt>任务ID</dt><dd>${escapeHtml(String(task.id || '-'))}</dd></div>
        <div><dt>状态</dt><dd>${escapeHtml(archiveTaskStatusText(task.status || ''))}</dd></div>
        <div><dt>创建时间</dt><dd>${escapeHtml(createdAt)}</dd></div>
        <div><dt>开始时间</dt><dd>${escapeHtml(startedAt)}</dd></div>
        <div><dt>结束时间</dt><dd>${escapeHtml(finishedAt)}</dd></div>
        <div><dt>当前压缩包</dt><dd>${escapeHtml(currentArchive || '-')}</dd></div>
        <div class="full"><dt>源目录</dt><dd>${escapeHtml(sourcePath)}</dd></div>
        <div class="full"><dt>输出目录</dt><dd>${escapeHtml(outputDir)}</dd></div>
        ${errorText ? `<div class="full"><dt>错误</dt><dd class="error-text">${escapeHtml(errorText)}</dd></div>` : ''}
      </dl>
    `;
  }

  function webpTaskItems() {
    const running = state.webpConvert.runningTask && state.webpConvert.runningTask.id ? [state.webpConvert.runningTask] : [];
    const items = Array.isArray(state.webpConvert.items) ? state.webpConvert.items : [];
    return running.concat(items);
  }

  function renderWebPTaskList() {
    if (!el.webpConvertTaskList) return;
    const items = webpTaskItems();
    el.webpConvertTaskList.innerHTML = '';
    if (!items.length) {
      el.webpConvertTaskList.innerHTML = '<li><span class="hint">暂无转换任务</span></li>';
      return;
    }
    const frag = document.createDocumentFragment();
    items.forEach((item) => {
      const li = document.createElement('li');
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = `archive-task-item${String(item.id || '') === String(state.webpConvert.selectedTaskID || '') ? ' active' : ''}`;
      const status = archiveTaskStatusText(item.status);
      const total = Number(item.progress_total || 0);
      const done = Number(item.progress_done || 0);
      btn.innerHTML = `
        <span class="archive-task-title">${status} · ${done}/${total || 0}</span>
        <span class="archive-task-line">任务ID: ${String(item.id || '-')}</span>
        <span class="archive-task-line">源目录: ${String(item.source_path || '-')}</span>
        <span class="archive-task-line">输出目录: ${String(item.output_dir || '-')}</span>
      `;
      btn.addEventListener('click', () => {
        void selectWebPTask(item.id, true);
      });
      li.appendChild(btn);
      frag.appendChild(li);
    });
    el.webpConvertTaskList.appendChild(frag);
  }

  function renderWebPTaskProgress(task) {
    const total = Number(task?.progress_total || 0);
    const done = Number(task?.progress_done || 0);
    const percent = total > 0 ? Math.max(0, Math.min(100, Math.floor(done * 100 / total))) : 0;
    if (el.webpConvertProgressFill) {
      el.webpConvertProgressFill.style.width = `${percent}%`;
    }
    const status = String(task?.status || '').toLowerCase();
    const cancelRequested = !!task?.cancel_requested;
    const active = status === 'queued' || status === 'running';
    if (el.webpConvertCancelBtn) {
      el.webpConvertCancelBtn.disabled = !active || cancelRequested;
    }
    if (el.webpConvertProgressText) {
      const current = String(task?.current_item || '').trim();
      const phase = String(task?.current_phase || '').trim();
      const statusText = cancelRequested && active ? '取消中' : archiveTaskStatusText(task?.status || '');
      el.webpConvertProgressText.textContent = `${statusText} · ${done}/${total}${phase ? ` · ${phase}` : ''}${current ? ` · ${current}` : ''}`;
    }
    if (el.webpConvertTaskMeta) {
      const summary = task?.summary || {};
      el.webpConvertTaskMeta.textContent = `扫描 ${Number(summary.scanned || 0)} · 命中 ${Number(summary.matched || 0)} · 成功 ${Number(summary.moved || 0)} · 转换图片 ${Number(summary.extracted || 0)} · 保留 WebP ${Number(summary.kept || 0)} · 失败 ${Number(summary.failed || 0)}`;
    }
    renderWebPTaskSummary(task);
  }

  async function loadWebPTasks() {
    if (!isAdmin) return;
    const data = await fetchJSON('api/admin/webp/tasks');
    state.webpConvert.runningTask = data?.running && data.running.id ? data.running : null;
    state.webpConvert.items = Array.isArray(data?.items) ? data.items : [];
    renderWebPTaskList();

    const selectedID = String(state.webpConvert.selectedTaskID || '').trim();
    if (!selectedID) {
      const next = webpTaskItems();
      if (next.length > 0) {
        await selectWebPTask(next[0].id, true);
      } else {
        state.webpConvert.lastSeq = 0;
        state.webpConvert.finalRenderedID = '';
        renderWebPTaskProgress(null);
      }
      return;
    }

    const found = webpTaskItems().some((item) => String(item.id || '') === selectedID);
    if (!found) {
      state.webpConvert.selectedTaskID = '';
      state.webpConvert.lastSeq = 0;
      state.webpConvert.finalRenderedID = '';
      const next = webpTaskItems();
      if (next.length > 0) {
        await selectWebPTask(next[0].id, true);
        return;
      }
      renderWebPTaskProgress(null);
      return;
    }

    await loadWebPTaskDetail(false);
    ensureWebPTaskPolling();
  }

  async function loadWebPTaskDetail(forceResetLog) {
    const taskID = String(state.webpConvert.selectedTaskID || '').trim();
    if (!taskID) return;
    const data = await fetchJSON(`api/admin/webp/tasks/${encodeURIComponent(taskID)}`);
    const task = data?.task || null;
    if (!task) return;
    renderWebPTaskProgress(task);
    if (forceResetLog) {
      state.webpConvert.lastSeq = 0;
      state.webpConvert.finalRenderedID = '';
    }
  }

  async function selectWebPTask(taskID, forceResetLog) {
    const nextID = String(taskID || '').trim();
    if (!nextID) return;
    if (String(state.webpConvert.selectedTaskID || '') !== nextID) {
      state.webpConvert.lastSeq = 0;
      state.webpConvert.finalRenderedID = '';
    }
    state.webpConvert.selectedTaskID = nextID;
    renderWebPTaskList();
    await loadWebPTaskDetail(!!forceResetLog);
  }

  function selectedRootDirectoryByID(selectEl) {
    if (!selectEl) return null;
    const selectedID = Number(selectEl.value || 0);
    if (!Number.isFinite(selectedID) || selectedID <= 0) return null;
    return state.rootDirectories.find((item) => Number(item.id) === selectedID) || null;
  }

  function selectedWebPSourceRootPath() {
    return normalizeRootPath(selectedRootDirectoryByID(el.webpConvertSourceRoot)?.path || '/');
  }

  function selectedWebPOutputRootPath() {
    const root = selectedRootDirectoryByID(el.webpConvertOutputRoot);
    return root ? normalizeRootPath(root.path || '/') : '';
  }

  async function loadWebPSourceTree(pathValue) {
    if (!el.webpConvertTreeRoot) return;
    const root = selectedRootDirectoryByID(el.webpConvertSourceRoot);
    if (!root) {
      alert('请先选择一个源根目录');
      return;
    }
    const data = await fetchJSON(`api/admin/root-directories/${root.id}/browse`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: String(pathValue || '') })
    });
    renderWebPSourceTree(data);
    if (!String(pathValue || '').trim() && el.webpConvertSourcePath) {
      el.webpConvertSourcePath.value = String(data?.current_path || selectedWebPSourceRootPath());
    }
  }

  function renderWebPSourceTree(data) {
    if (!el.webpConvertTreeRoot) return;
    const list = document.createElement('ul');
    list.className = 'mount-tree-list';
    const items = Array.isArray(data?.items) ? data.items : [];
    const currentPath = String(data?.current_path || '').trim();
    if (el.webpConvertBrowsePath) el.webpConvertBrowsePath.value = currentPath;
    if (!items.length) {
      const empty = document.createElement('li');
      empty.innerHTML = '<span class="hint">当前目录无子目录</span>';
      list.appendChild(empty);
    } else {
      items.forEach((item) => {
        list.appendChild(buildWebPSourceTreeNode({
          name: String(item.name || item.path || '/'),
          path: String(item.path || '')
        }));
      });
    }
    el.webpConvertTreeRoot.innerHTML = '';
    el.webpConvertTreeRoot.appendChild(list);
  }

  function buildWebPSourceTreeNode(node) {
    const li = document.createElement('li');
    li.className = 'mount-tree-node';
    li.dataset.path = node.path;

    const row = document.createElement('div');
    row.className = 'mount-tree-row';
    const expanded = state.webpConvert.treeExpanded.has(node.path);

    const toggleBtn = document.createElement('button');
    toggleBtn.type = 'button';
    toggleBtn.className = 'mount-tree-toggle';
    toggleBtn.textContent = expanded ? '▾' : '▸';
    row.appendChild(toggleBtn);

    const label = document.createElement('button');
    label.type = 'button';
    label.className = 'mount-tree-label';
    label.textContent = node.name;
    row.appendChild(label);

    const selectBtn = document.createElement('button');
    selectBtn.type = 'button';
    selectBtn.className = 'btn';
    selectBtn.textContent = '选中';
    selectBtn.addEventListener('click', () => {
      if (el.webpConvertSourcePath) el.webpConvertSourcePath.value = node.path;
    });
    row.appendChild(selectBtn);
    li.appendChild(row);

    const childWrap = document.createElement('ul');
    childWrap.className = 'mount-tree-list';
    childWrap.hidden = !expanded;
    li.appendChild(childWrap);

    const expandNode = async () => {
      if (state.webpConvert.treeExpanded.has(node.path)) {
        state.webpConvert.treeExpanded.delete(node.path);
        childWrap.hidden = true;
        toggleBtn.textContent = '▸';
        return;
      }
      state.webpConvert.treeExpanded.add(node.path);
      toggleBtn.textContent = '▾';
      if (childWrap.childElementCount === 0) {
        const root = selectedRootDirectoryByID(el.webpConvertSourceRoot);
        if (!root) return;
        const data = await fetchJSON(`api/admin/root-directories/${root.id}/browse`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ path: node.path })
        });
        const items = Array.isArray(data?.items) ? data.items : [];
        if (!items.length) {
          const empty = document.createElement('li');
          empty.innerHTML = '<span class="hint">无子目录</span>';
          childWrap.appendChild(empty);
        } else {
          items.forEach((item) => {
            childWrap.appendChild(buildWebPSourceTreeNode({
              name: String(item.name || item.path || '/'),
              path: String(item.path || '')
            }));
          });
        }
      }
      childWrap.hidden = false;
      if (el.webpConvertBrowsePath) el.webpConvertBrowsePath.value = node.path;
    };

    toggleBtn.addEventListener('click', () => {
      void expandNode();
    });
    label.addEventListener('click', () => {
      void expandNode();
    });
    return li;
  }

  async function saveWebPDefaults() {
    state.runtime.webpQuality = Math.min(100, Math.max(1, Math.floor(Number(el.webpConvertQuality?.value || state.runtime.webpQuality))));
    state.runtime.webpMethod = Math.min(6, Math.max(0, Math.floor(Number(el.webpConvertMethod?.value || state.runtime.webpMethod))));
    state.runtime.webpConcurrency = Math.min(16, Math.max(1, Math.floor(Number(el.webpConvertConcurrency?.value || state.runtime.webpConcurrency))));
    state.runtime.webpOutputRoot = selectedWebPOutputRootPath();
    await saveRuntimeSettingsNow();
    renderWebPOutputRootOptions();
    syncRuntimeSettingsToControls();
  }

  async function runWebPConvertTask() {
    const sourceRootID = Number(el.webpConvertSourceRoot?.value || 0);
    const outputRootID = Number(el.webpConvertOutputRoot?.value || 0);
    const sourcePath = String(el.webpConvertSourcePath?.value || '').trim();
    const webpQuality = Math.min(100, Math.max(1, Math.floor(Number(el.webpConvertQuality?.value || 75))));
    const webpMethod = Math.min(6, Math.max(0, Math.floor(Number(el.webpConvertMethod?.value || 0))));
    const webpConcurrency = Math.min(16, Math.max(1, Math.floor(Number(el.webpConvertConcurrency?.value || 2))));
    if (!Number.isFinite(sourceRootID) || sourceRootID <= 0) {
      alert('请先选择源根目录');
      return;
    }
    if (!sourcePath) {
      alert('请先从目录树中选择扫描源目录');
      return;
    }
    if (!Number.isFinite(outputRootID) || outputRootID <= 0) {
      alert('请先选择输出根目录');
      return;
    }

    if (el.webpConvertRunBtn) el.webpConvertRunBtn.disabled = true;
    try {
      const data = await fetchJSON('api/admin/webp/tasks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          source_root_id: sourceRootID,
          source_path: sourcePath,
          output_root_id: outputRootID,
          webp_quality: webpQuality,
          webp_method: webpMethod,
          webp_concurrency: webpConcurrency
        })
      });
      const task = data?.task || null;
      if (!task?.id) {
        throw new Error('后台任务创建失败：未返回任务ID');
      }
      state.webpConvert.selectedTaskID = String(task.id);
      state.webpConvert.lastSeq = 0;
      state.webpConvert.finalRenderedID = '';
      await loadWebPTasks();
      await selectWebPTask(task.id, true);
      ensureWebPTaskPolling();
    } catch (err) {
      alert('执行失败: ' + (err?.message || String(err)));
    } finally {
      if (el.webpConvertRunBtn) el.webpConvertRunBtn.disabled = false;
    }
  }

  async function cancelWebPTask() {
    const taskID = String(state.webpConvert.selectedTaskID || '').trim();
    if (!taskID) {
      alert('请先选择一个任务');
      return;
    }
    if (!confirm(`确认取消 WebP 转换任务 ${taskID} ?`)) {
      return;
    }
    if (el.webpConvertCancelBtn) el.webpConvertCancelBtn.disabled = true;
    try {
      await fetchJSON(`api/admin/webp/tasks/${encodeURIComponent(taskID)}/cancel`, {
        method: 'POST'
      });
    } catch (err) {
      alert('取消失败: ' + (err?.message || String(err)));
    } finally {
      await loadWebPTasks().catch(() => {});
      ensureWebPTaskPolling();
    }
  }

  function hasActiveWebPTask() {
    return webpTaskItems().some((item) => {
      const status = String(item?.status || '').toLowerCase();
      return status === 'queued' || status === 'running';
    });
  }

  function ensureWebPTaskPolling() {
    if (!isAdmin) return;
    if (state.webpConvert.pollTimer) return;
    if (!hasActiveWebPTask()) return;
    state.webpConvert.pollTimer = setInterval(async () => {
      try {
        await loadWebPTasks();
        if (!hasActiveWebPTask() && state.webpConvert.pollTimer) {
          clearInterval(state.webpConvert.pollTimer);
          state.webpConvert.pollTimer = null;
        }
      } catch (_) {}
    }, 1800);
  }

  async function runArchiveWorkflow(action) {
    if (!isAdmin) return;
    if (!el.archiveWorkflowMount) {
      alert('未找到压缩包处理挂载选择器');
      return;
    }
    const mountID = Number(el.archiveWorkflowMount.value || 0);
    if (!Number.isFinite(mountID) || mountID <= 0) {
      alert('请先选择一个 local 挂载');
      return;
    }

    const password = String(el.archiveWorkflowPassword?.value || '');
    setArchiveWorkflowButtonsDisabled(true);
    appendArchiveWorkflowOutput(`[${new Date().toLocaleString()}] 创建后台任务: ${archiveActionText(action)} ...`);

    try {
      const data = await fetchJSON('api/admin/archive-workflow/tasks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          action: String(action || '').trim(),
          mount_id: mountID,
          password
        })
      });
      const task = data?.task || null;
      if (!task?.id) {
        throw new Error('任务创建成功但未返回任务ID');
      }
      await loadArchiveTasks();
      await selectArchiveTask(task.id, true);
      ensureArchiveTaskPolling();
    } catch (err) {
      appendArchiveWorkflowOutput(`[${new Date().toLocaleString()}] 执行失败: ${err?.message || String(err)}`);
      alert('执行失败: ' + (err?.message || String(err)));
    } finally {
      setArchiveWorkflowButtonsDisabled(false);
    }
  }

  function formatSize(n) {
    const size = Number(n) || 0;
    if (size < 1024) return `${size}B`;
    if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)}KB`;
    if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)}MB`;
    return `${(size / 1024 / 1024 / 1024).toFixed(2)}GB`;
  }

  function escapeHtml(input) {
    return String(input)
      .replaceAll('&', '&amp;')
      .replaceAll('<', '&lt;')
      .replaceAll('>', '&gt;')
      .replaceAll('"', '&quot;')
      .replaceAll("'", '&#39;');
  }

  function favoriteLabel(isFavorite) {
    return isFavorite ? '取消收藏' : '加入收藏';
  }

  function syncFavoriteButton(button, isFavorite, variant) {
    if (!button) return;
    const active = Boolean(isFavorite);
    const label = favoriteLabel(active);
    button.classList.toggle('active', active);
    button.setAttribute('aria-label', label);
    button.title = label;
    if (variant === 'reader') {
      button.textContent = active ? '已收藏' : '收藏';
    } else {
      button.textContent = '★';
    }
  }

  function syncReaderFavoriteButton() {
    syncFavoriteButton(el.readerFavoriteBtn, state.reader.comic?.is_favorite, 'reader');
  }

  function bindFavoriteToggle(button, comic, afterChange, variant) {
    if (!button || !comic?.id) return;
    syncFavoriteButton(button, comic.is_favorite, variant);
    button.addEventListener('click', async (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      const nextFavorite = !Boolean(comic.is_favorite);
      button.disabled = true;
      try {
        await setComicFavorite(comic.id, nextFavorite, comic.file_name_key);
        comic.is_favorite = nextFavorite;
        syncFavoriteButton(button, nextFavorite, variant);
        if (typeof afterChange === 'function') {
          await afterChange(nextFavorite);
        }
      } catch (err) {
        alert('更新收藏失败: ' + (err?.message || String(err)));
      } finally {
        button.disabled = false;
      }
    });
  }

  function parseSortRule(v) {
    const raw = String(v || 'updated_desc').trim();
    const pos = raw.lastIndexOf('_');
    if (pos <= 0) return { sort: 'updated_at', order: 'desc', value: 'updated_desc' };
    const sortRaw = raw.slice(0, pos);
    const orderRaw = raw.slice(pos + 1).toLowerCase() === 'asc' ? 'asc' : 'desc';
    const sortMap = {
      updated: 'updated_at',
      title: 'title',
      size: 'size',
      page_count: 'page_count',
      mtime_unix: 'mtime_unix',
      last_read: 'last_read'
    };
    const sort = sortMap[sortRaw] || 'updated_at';
    return { sort, order: orderRaw, value: `${sortRaw}_${orderRaw}` };
  }

  function normalizeReadingStatus(c) {
    const s = String(c.reading_status || '').toLowerCase();
    if (s === 'finished' || c.is_finished) return 'finished';
    if (s === 'reading' || c.has_progress) return 'reading';
    return 'unread';
  }

  function normalizeLibraryPageSize(value) {
    const n = Number(value);
    if (!Number.isFinite(n)) return DEFAULT_LIBRARY_PAGE_SIZE;
    return LIBRARY_PAGE_SIZE_OPTIONS.includes(n) ? n : DEFAULT_LIBRARY_PAGE_SIZE;
  }

  function normalizeRuntimeSettings(payload) {
    const memoryRaw = Number(payload?.memory_limit_mb ?? payload?.memoryLimitMB ?? DEFAULT_RUNTIME_SETTINGS.memoryLimitMB);
    const diskRaw = Number(payload?.disk_cache_mb ?? payload?.diskCacheMB ?? DEFAULT_RUNTIME_SETTINGS.diskCacheMB);
    const prefetchRaw = Number(payload?.page_prefetch_pages ?? payload?.pagePrefetchPages ?? DEFAULT_RUNTIME_SETTINGS.pagePrefetchPages);
    const pageLoadParallelismRaw = Number(
      payload?.page_load_parallelism
        ?? payload?.pageLoadParallelism
        ?? DEFAULT_RUNTIME_SETTINGS.pageLoadParallelism
    );
    const memoryLimitMB = Number.isFinite(memoryRaw) ? Math.min(32768, Math.max(256, Math.floor(memoryRaw))) : DEFAULT_RUNTIME_SETTINGS.memoryLimitMB;
    const diskCacheMB = Number.isFinite(diskRaw) ? Math.min(1048576, Math.max(256, Math.floor(diskRaw))) : DEFAULT_RUNTIME_SETTINGS.diskCacheMB;
    const pagePrefetchPages = Number.isFinite(prefetchRaw) ? Math.min(100, Math.max(1, Math.floor(prefetchRaw))) : DEFAULT_RUNTIME_SETTINGS.pagePrefetchPages;
    const pageLoadParallelism = Number.isFinite(pageLoadParallelismRaw)
      ? Math.min(16, Math.max(1, Math.floor(pageLoadParallelismRaw)))
      : DEFAULT_RUNTIME_SETTINGS.pageLoadParallelism;
    const webpQualityRaw = Number(payload?.webp_quality ?? payload?.webpQuality ?? DEFAULT_RUNTIME_SETTINGS.webpQuality);
    const webpMethodRaw = Number(payload?.webp_method ?? payload?.webpMethod ?? DEFAULT_RUNTIME_SETTINGS.webpMethod);
    const webpConcurrencyRaw = Number(payload?.webp_concurrency ?? payload?.webpConcurrency ?? DEFAULT_RUNTIME_SETTINGS.webpConcurrency);
    const webpQuality = Number.isFinite(webpQualityRaw) ? Math.min(100, Math.max(1, Math.floor(webpQualityRaw))) : DEFAULT_RUNTIME_SETTINGS.webpQuality;
    const webpMethod = Number.isFinite(webpMethodRaw) ? Math.min(6, Math.max(0, Math.floor(webpMethodRaw))) : DEFAULT_RUNTIME_SETTINGS.webpMethod;
    const webpConcurrency = Number.isFinite(webpConcurrencyRaw) ? Math.min(16, Math.max(1, Math.floor(webpConcurrencyRaw))) : DEFAULT_RUNTIME_SETTINGS.webpConcurrency;
    const webpOutputRoot = String((payload?.webp_output_root ?? payload?.webpOutputRoot ?? DEFAULT_RUNTIME_SETTINGS.webpOutputRoot) || '').trim();
    return { memoryLimitMB, diskCacheMB, pagePrefetchPages, pageLoadParallelism, webpQuality, webpMethod, webpConcurrency, webpOutputRoot };
  }

  function syncRuntimeSettingsToControls() {
    if (!el.runtimeMemoryLimitMB) return;
    el.runtimeMemoryLimitMB.value = String(state.runtime.memoryLimitMB);
    if (el.runtimeDiskCacheMB) el.runtimeDiskCacheMB.value = String(state.runtime.diskCacheMB);
    if (el.runtimePagePrefetchPages) el.runtimePagePrefetchPages.value = String(state.runtime.pagePrefetchPages);
    if (el.runtimePageLoadParallelism) {
      el.runtimePageLoadParallelism.value = String(state.runtime.pageLoadParallelism);
    }
    if (el.webpConvertQuality) el.webpConvertQuality.value = String(state.runtime.webpQuality);
    if (el.webpConvertMethod) el.webpConvertMethod.value = String(state.runtime.webpMethod);
    if (el.webpConvertConcurrency) el.webpConvertConcurrency.value = String(state.runtime.webpConcurrency);
    if (el.webpConvertOutputRoot) {
      const matched = state.rootDirectories.find((item) => String(item.path || '').trim() === String(state.runtime.webpOutputRoot || '').trim());
      if (matched) {
        el.webpConvertOutputRoot.value = String(matched.id);
      }
    }
  }

  async function loadRuntimeSettings() {
    const data = await fetchJSON('api/admin/runtime');
    if (data?.runtime) {
      state.runtime = normalizeRuntimeSettings(data.runtime);
    } else {
      state.runtime = { ...DEFAULT_RUNTIME_SETTINGS };
    }
    syncRuntimeSettingsToControls();
  }

  async function saveRuntimeSettingsNow() {
    const payload = {
      memory_limit_mb: state.runtime.memoryLimitMB,
      disk_cache_mb: state.runtime.diskCacheMB,
      page_prefetch_pages: state.runtime.pagePrefetchPages,
      page_load_parallelism: state.runtime.pageLoadParallelism,
      webp_quality: state.runtime.webpQuality,
      webp_method: state.runtime.webpMethod,
      webp_concurrency: state.runtime.webpConcurrency,
      webp_output_root: state.runtime.webpOutputRoot
    };
    const data = await fetchJSON('api/admin/runtime', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    if (data?.runtime) {
      state.runtime = normalizeRuntimeSettings(data.runtime);
      syncRuntimeSettingsToControls();
    }
  }

  function captureLibraryState(scrollYOverride) {
    return {
      q: el.searchInput ? el.searchInput.value.trim() : '',
      mount_id: el.mountFilter ? String(el.mountFilter.value || '') : '',
      folder: el.folderFilter ? String(el.folderFilter.value || '') : '',
      sort_rule: el.sortRule ? String(el.sortRule.value || 'updated_desc') : 'updated_desc',
      status: el.statusFilter ? String(el.statusFilter.value || 'all') : 'all',
      page: Math.max(1, Number(state.library.page || 1)),
      page_size: normalizeLibraryPageSize(state.library.pageSize),
      scroll_y: Number.isFinite(scrollYOverride) ? Math.max(0, Math.floor(scrollYOverride)) : Math.max(0, Math.floor(window.scrollY || 0)),
      saved_at: Date.now()
    };
  }

  function saveLibraryState(scrollYOverride) {
    try {
      localStorage.setItem(LIBRARY_STATE_KEY, JSON.stringify(captureLibraryState(scrollYOverride)));
    } catch (_) {}
  }

  function restoreLibraryState() {
    try {
      const raw = localStorage.getItem(LIBRARY_STATE_KEY);
      if (!raw) return;
      const saved = JSON.parse(raw);
      if (!saved || typeof saved !== 'object') return;
      if (el.searchInput && typeof saved.q === 'string') el.searchInput.value = saved.q;
      if (el.sortRule && typeof saved.sort_rule === 'string') {
        const found = [...el.sortRule.options].some(o => o.value === saved.sort_rule);
        if (found) el.sortRule.value = saved.sort_rule;
      }
      if (el.statusFilter && typeof saved.status === 'string') {
        const found = [...el.statusFilter.options].some(o => o.value === saved.status);
        if (found) el.statusFilter.value = saved.status;
      }
      if (el.mountFilter && typeof saved.mount_id === 'string') {
        const found = [...el.mountFilter.options].some(o => o.value === saved.mount_id);
        if (found) el.mountFilter.value = saved.mount_id;
      }
      if (el.folderFilter && typeof saved.folder === 'string') {
        el.folderFilter.dataset.pendingValue = saved.folder;
      }
      const savedPageSize = normalizeLibraryPageSize(saved.page_size);
      state.library.pageSize = savedPageSize;
      if (el.libraryPageSize) el.libraryPageSize.value = String(savedPageSize);
      if (Number.isFinite(Number(saved.page))) {
        state.library.page = Math.max(1, Math.floor(Number(saved.page)));
      }
      if (Number.isFinite(Number(saved.scroll_y))) {
        libraryPendingScrollY = Math.max(0, Number(saved.scroll_y));
      }
    } catch (_) {}
  }

  function applyPendingLibraryScroll() {
    if (libraryPendingScrollY === null) return;
    const y = libraryPendingScrollY;
    libraryPendingScrollY = null;
    setTimeout(() => window.scrollTo({ top: y, behavior: 'auto' }), 0);
  }

  function openReader(comicID) {
    if (!comicID) return;
    if (view === 'library') {
      saveLibraryState(window.scrollY || 0);
    }
    window.open(appURL(`reader/${comicID}`), '_blank');
  }

  async function logout() {
    await fetchJSON('api/logout', { method: 'POST' });
    location.href = appURL('login');
  }

  function closeReader() {
    if (isReaderFullscreenActive()) {
      state.reader.pseudoFullscreen = false;
      document.body.classList.remove('fullscreen-reading');
      if (document.fullscreenElement) {
        unlockOrientationIfPossible();
        document.exitFullscreen().catch(() => {});
        return;
      } else {
        unlockOrientationIfPossible();
      }
      updateFullscreenButton();
      renderReaderByMode(true);
      return;
    }
    state.reader.pseudoFullscreen = false;
    document.body.classList.remove('fullscreen-reading');
    void destroyReaderPDF();
    location.href = appURL('./');
  }

  function onLibraryScroll() {
    if (view !== 'library') return;
    if (libraryScrollTimer) clearTimeout(libraryScrollTimer);
    libraryScrollTimer = setTimeout(() => {
      libraryScrollTimer = null;
      saveLibraryState(window.scrollY || 0);
    }, 140);
  }

  async function loadMounts() {
    const data = await fetchJSON('api/mounts');
    if (!data) return;
    state.mounts = data.items || [];

    const enabledMounts = state.mounts.filter(m => m.enabled);
    [el.mountFilter, el.favoritesMountFilter].forEach((selectEl) => {
      if (!selectEl) return;
      const current = String(selectEl.value || '');
      selectEl.innerHTML = '';
      const all = document.createElement('option');
      all.value = '';
      all.textContent = '全部挂载';
      selectEl.appendChild(all);
      enabledMounts.forEach((m) => {
        const opt = document.createElement('option');
        opt.value = String(m.id);
        opt.textContent = `${m.name} (${mountTypeName(m.type)})`;
        selectEl.appendChild(opt);
      });
      if ([...selectEl.options].some(o => o.value === current)) selectEl.value = current;
      applyPendingSelectValue(selectEl, current);
    });

    if (isAdmin) {
      if (el.mountList) renderMountList();
      renderArchiveWorkflowMountOptions();
      renderWebPSourceRootOptions();
      renderWebPOutputRootOptions();
    }
  }

  async function loadComicFolderOptions(selectEl, mountIDValue, options) {
    if (!selectEl) return;
    const opts = options || {};
    const current = String(selectEl.value || '');
    const pending = typeof selectEl.dataset.pendingValue === 'string' ? selectEl.dataset.pendingValue : '';
    const mountID = String(mountIDValue || '').trim();
    selectEl.innerHTML = '';

    const all = document.createElement('option');
    all.value = '';
    all.textContent = mountID ? '全部文件夹' : '先选择挂载';
    selectEl.appendChild(all);

    if (!mountID) {
      selectEl.value = '';
      selectEl.disabled = true;
      delete selectEl.dataset.pendingValue;
      return;
    }

    const params = new URLSearchParams({ mount_id: mountID });
    if (opts.favoriteOnly) params.set('favorite', 'favorite');
    const data = await fetchJSON(`api/comics/folders?${params.toString()}`);
    const items = Array.isArray(data?.items) ? data.items : [];
    items.forEach((item) => {
      const opt = document.createElement('option');
      opt.value = String(item?.value || '');
      opt.textContent = String(item?.label || item?.value || '');
      selectEl.appendChild(opt);
    });
    selectEl.disabled = false;

    const nextValue = pending || current;
    if (nextValue && [...selectEl.options].some(o => o.value === nextValue)) {
      selectEl.value = nextValue;
    } else {
      selectEl.value = '';
    }
    delete selectEl.dataset.pendingValue;
  }

  function renderComicCards(container, items, options) {
    const opts = options || {};
    container.innerHTML = '';
    if (!items.length) {
      container.innerHTML = '<p class="empty-tip">暂无内容</p>';
      return;
    }

    const frag = document.createDocumentFragment();
    items.forEach((c, idx) => {
      const readStatus = normalizeReadingStatus(c);
      const currentPage = Math.max(0, Number(c.last_page || 0) + 1);
      const totalPages = Math.max(0, Number(c.progress_total_pages || c.page_count || 0));
      const progressPercent = Math.max(0, Math.min(100, Number(c.progress_percent || 0)));
      const fileFormat = String(c.format || '').toLowerCase();
      let hint = `${c.page_count || '?'} 页 · ${formatSize(c.size)}`;
      if (fileFormat === 'pdf' && (!c.page_count || Number(c.page_count) <= 0)) {
        hint = `PDF · ${formatSize(c.size)}`;
      }
      if (readStatus === 'finished') {
        hint = `已读完 · ${totalPages || c.page_count || '?'} 页`;
      } else if (readStatus === 'reading') {
        hint = `阅读进度 ${currentPage}/${totalPages || '?'}`;
      }
      const badge = readStatus === 'finished'
        ? '<span class="badge finished">已读完</span>'
        : (readStatus === 'reading' ? '<span class="badge reading">阅读中</span>' : '');
      const card = document.createElement('article');
      card.className = `card ${readStatus}`;
      card.style.animationDelay = `${Math.min(idx * 10, 220)}ms`;
      const favoriteClass = c.is_favorite ? 'active' : '';
      const coverLoading = idx < 10 ? 'eager' : 'lazy';
      const coverPriority = idx < 6 ? 'high' : 'auto';
      card.innerHTML = `
        <img class="cover" loading="${coverLoading}" fetchpriority="${coverPriority}" decoding="async" src="${c.cover_url}" alt="${escapeHtml(c.title)}" />
        <div class="meta">
          <div class="badge-row">${badge}
            <button class="favorite-btn ${favoriteClass}" type="button" aria-label="${favoriteLabel(c.is_favorite)}" title="${favoriteLabel(c.is_favorite)}">★</button>
          </div>
          <div class="title" title="${escapeHtml(c.title)}">${escapeHtml(c.title)}</div>
          <div class="hint">${escapeHtml(hint)}</div>
          <div class="progress-track" ${readStatus === 'unread' ? 'hidden' : ''}>
            <div class="progress-fill" style="width:${progressPercent}%"></div>
          </div>
        </div>`;
      const coverImg = card.querySelector('img.cover');
      if (coverImg && fileFormat === 'pdf') {
        coverImg.dataset.comicId = String(c.id);
        coverImg.dataset.coverCacheKey = `${c.id}:${c.mtime_unix}:${c.size}`;
        coverImg.dataset.fileFormat = 'pdf';
        observePDFCoverImage(coverImg);
      }
      const favoriteBtn = card.querySelector('.favorite-btn');
      if (favoriteBtn) {
        bindFavoriteToggle(favoriteBtn, c, async () => {
          if (opts.context === 'favorites') {
            await loadFavoriteComics();
          } else if (opts.context === 'library') {
            const currentY = window.scrollY || 0;
            await loadComics();
            saveLibraryState(currentY);
          }
        }, 'card');
      }
      card.addEventListener('click', () => openReader(c.id));
      frag.appendChild(card);
    });
    container.appendChild(frag);
  }

  async function setComicFavorite(comicID, favorite, fileNameKey) {
    if (!comicID) return;
    const method = favorite ? 'POST' : 'DELETE';
    const normalizedKey = String(fileNameKey || '').trim();
    const url = normalizedKey
      ? `api/comics/${comicID}/favorite?${new URLSearchParams({ file_name_key: normalizedKey }).toString()}`
      : `api/comics/${comicID}/favorite`;
    await fetchJSON(url, { method });

    state.comics = state.comics.map((item) => {
      const sameID = Number(item.id) === Number(comicID);
      const sameKey = normalizedKey && String(item.file_name_key || '') === normalizedKey;
      if (!sameID && !sameKey) return item;
      return { ...item, is_favorite: favorite };
    });
    state.favorites.comics = state.favorites.comics
      .map((item) => {
        const sameID = Number(item.id) === Number(comicID);
        const sameKey = normalizedKey && String(item.file_name_key || '') === normalizedKey;
        if (!sameID && !sameKey) return item;
        return { ...item, is_favorite: favorite };
      })
      .filter((item) => Boolean(item.is_favorite));
    if (state.reader.comic && (Number(state.reader.comic.id) === Number(comicID) || (normalizedKey && String(state.reader.comic.file_name_key || '') === normalizedKey))) {
      state.reader.comic.is_favorite = favorite;
      syncReaderFavoriteButton();
    }
  }

  function updateLibraryPagerUI() {
    const totalPages = Math.max(0, Number(state.library.totalPages || 0));
    const currentPage = totalPages > 0 ? Math.min(Math.max(1, Number(state.library.page || 1)), totalPages) : 1;
    if (el.libraryPageText) {
      el.libraryPageText.textContent = `第 ${currentPage} / ${Math.max(1, totalPages)} 页`;
    }
    if (el.libraryPrevPageBtn) el.libraryPrevPageBtn.disabled = currentPage <= 1 || totalPages <= 0;
    if (el.libraryNextPageBtn) el.libraryNextPageBtn.disabled = totalPages <= 0 || currentPage >= totalPages;
    if (el.libraryPageSize) el.libraryPageSize.value = String(normalizeLibraryPageSize(state.library.pageSize));
  }

  function updateFavoritesPagerUI() {
    const totalPages = Math.max(0, Number(state.favorites.totalPages || 0));
    const currentPage = totalPages > 0 ? Math.min(Math.max(1, Number(state.favorites.page || 1)), totalPages) : 1;
    if (el.favoritesPageText) {
      el.favoritesPageText.textContent = `第 ${currentPage} / ${Math.max(1, totalPages)} 页`;
    }
    if (el.favoritesPrevPageBtn) el.favoritesPrevPageBtn.disabled = currentPage <= 1 || totalPages <= 0;
    if (el.favoritesNextPageBtn) el.favoritesNextPageBtn.disabled = totalPages <= 0 || currentPage >= totalPages;
    if (el.favoritesPageSize) el.favoritesPageSize.value = String(normalizeLibraryPageSize(state.favorites.pageSize));
  }

  async function changeLibraryPage(page, shouldScrollTop) {
    const target = Math.max(1, Math.floor(Number(page) || 1));
    if (target === state.library.page) return;
    state.library.page = target;
    await loadComics();
    if (shouldScrollTop) window.scrollTo({ top: 0, behavior: 'auto' });
    saveLibraryState(0);
  }

  async function changeFavoritesPage(page, shouldScrollTop) {
    const target = Math.max(1, Math.floor(Number(page) || 1));
    if (target === state.favorites.page) return;
    state.favorites.page = target;
    await loadFavoriteComics();
    if (shouldScrollTop) window.scrollTo({ top: 0, behavior: 'auto' });
  }

  async function loadComics() {
    const params = new URLSearchParams();
    if (el.searchInput && el.searchInput.value.trim()) params.set('q', el.searchInput.value.trim());
    if (el.mountFilter && el.mountFilter.value) params.set('mount_id', el.mountFilter.value);
    if (el.statusFilter && el.statusFilter.value) params.set('status', el.statusFilter.value);
    if (el.folderFilter && el.folderFilter.value) params.set('folder', el.folderFilter.value);
    const sortRule = parseSortRule(el.sortRule ? el.sortRule.value : 'updated_desc');
    params.set('sort', sortRule.sort);
    params.set('order', sortRule.order);
    const pageSize = normalizeLibraryPageSize(state.library.pageSize);
    const page = Math.max(1, Math.floor(Number(state.library.page || 1)));
    params.set('page', String(page));
    params.set('page_size', String(pageSize));

    const data = await fetchJSON(`api/comics?${params.toString()}`);
    if (!data) return;
    state.comics = data.items || [];
    state.library.pageSize = pageSize;

    const pagination = data.pagination && typeof data.pagination === 'object' ? data.pagination : null;
    if (pagination) {
      state.library.total = Math.max(0, Number(pagination.total || 0));
      state.library.totalPages = Math.max(0, Number(pagination.total_pages || 0));
      state.library.page = Math.max(1, Number(pagination.page || page));
      state.library.pageSize = normalizeLibraryPageSize(pagination.page_size || pageSize);
    } else {
      // 兼容旧接口：没有分页元信息时，退化为单页。
      state.library.total = state.comics.length;
      state.library.totalPages = state.comics.length > 0 ? 1 : 0;
      state.library.page = 1;
    }
    if (state.library.totalPages > 0 && state.library.page > state.library.totalPages) {
      state.library.page = state.library.totalPages;
    }
    if (state.library.totalPages === 0) {
      state.library.page = 1;
    }
    updateLibraryPagerUI();

    if (el.summaryText) {
      const total = state.library.total;
      const finished = state.comics.filter(c => normalizeReadingStatus(c) === 'finished').length;
      const reading = state.comics.filter(c => normalizeReadingStatus(c) === 'reading').length;
      const zipCount = state.comics.filter(c => String(c.format || '').toLowerCase() === 'zip').length;
      const pdfCount = state.comics.filter(c => String(c.format || '').toLowerCase() === 'pdf').length;
      el.summaryText.textContent = total
        ? `共 ${total} 本 · 当前第 ${state.library.page}/${Math.max(1, state.library.totalPages)} 页 · 本页 ZIP ${zipCount} · PDF ${pdfCount} · 本页阅读中 ${reading} · 本页已读完 ${finished}`
        : '没有找到漫画';
    }
    if (el.comicGrid) renderComicCards(el.comicGrid, state.comics, { context: 'library' });
  }

  async function loadFavoriteComics() {
    const params = new URLSearchParams();
    const pageSize = normalizeLibraryPageSize(state.favorites.pageSize);
    const page = Math.max(1, Math.floor(Number(state.favorites.page || 1)));
    params.set('favorite', 'favorite');
    params.set('sort', 'updated_at');
    params.set('order', 'desc');
    if (el.favoritesMountFilter && el.favoritesMountFilter.value) params.set('mount_id', el.favoritesMountFilter.value);
    if (el.favoritesFolderFilter && el.favoritesFolderFilter.value) params.set('folder', el.favoritesFolderFilter.value);
    params.set('page', String(page));
    params.set('page_size', String(pageSize));

    const data = await fetchJSON(`api/comics?${params.toString()}`);
    if (!data) return;
    state.favorites.pageSize = pageSize;
    state.favorites.comics = data.items || [];

    const pagination = data.pagination && typeof data.pagination === 'object' ? data.pagination : null;
    if (pagination) {
      state.favorites.total = Math.max(0, Number(pagination.total || 0));
      state.favorites.totalPages = Math.max(0, Number(pagination.total_pages || 0));
      state.favorites.page = Math.max(1, Number(pagination.page || page));
      state.favorites.pageSize = normalizeLibraryPageSize(pagination.page_size || pageSize);
    } else {
      state.favorites.total = state.favorites.comics.length;
      state.favorites.totalPages = state.favorites.comics.length > 0 ? 1 : 0;
      state.favorites.page = 1;
    }
    if (state.favorites.totalPages > 0 && state.favorites.page > state.favorites.totalPages) {
      state.favorites.page = state.favorites.totalPages;
    }
    if (state.favorites.totalPages === 0) {
      state.favorites.page = 1;
    }
    updateFavoritesPagerUI();

    if (el.favoritesSummaryText) {
      el.favoritesSummaryText.textContent = state.favorites.total
        ? `已收藏 ${state.favorites.total} 本 · 当前第 ${state.favorites.page}/${Math.max(1, state.favorites.totalPages)} 页`
        : '尚未收藏漫画';
    }
    if (el.favoritesGrid) renderComicCards(el.favoritesGrid, state.favorites.comics, { context: 'favorites' });
  }

  async function loadHistory() {
    if (!el.historyGrid) return;
    const data = await fetchJSON('api/history?limit=100');
    if (!data) return;

    const items = (data.items || []).map(it => {
      const comic = it.comic;
      const p = it.progress || {};
      return {
        ...comic,
        page_count: p.total_pages || comic.page_count,
        title: `${comic.title}`,
        hint: `上次看到 ${(Number(p.last_page || 0) + 1)}${(p.total_pages || comic.page_count) ? '/' + (p.total_pages || comic.page_count) : ''}`
      };
    });

    el.historyGrid.innerHTML = '';
    if (!items.length) {
      el.historyGrid.innerHTML = '<p class="empty-tip">暂无历史记录</p>';
      return;
    }

    const frag = document.createDocumentFragment();
    items.forEach((c, idx) => {
      const card = document.createElement('article');
      card.className = 'card';
      card.style.animationDelay = `${Math.min(idx * 10, 220)}ms`;
      card.innerHTML = `
        <img class="cover" loading="lazy" src="${c.cover_url}" alt="${escapeHtml(c.title)}" />
        <div class="meta">
          <div class="badge-row">
            <button class="favorite-btn ${c.is_favorite ? 'active' : ''}" type="button" aria-label="${favoriteLabel(c.is_favorite)}" title="${favoriteLabel(c.is_favorite)}">★</button>
          </div>
          <div class="title" title="${escapeHtml(c.title)}">${escapeHtml(c.title)}</div>
          <div class="hint">${escapeHtml(c.hint)}</div>
        </div>`;
      const coverImg = card.querySelector('img.cover');
      if (coverImg && String(c.format || '').toLowerCase() === 'pdf') {
        coverImg.dataset.comicId = String(c.id);
        coverImg.dataset.coverCacheKey = `${c.id}:${c.mtime_unix}:${c.size}`;
        coverImg.dataset.fileFormat = 'pdf';
        observePDFCoverImage(coverImg);
      }
      const favoriteBtn = card.querySelector('.favorite-btn');
      if (favoriteBtn) {
        bindFavoriteToggle(favoriteBtn, c, null, 'card');
      }
      card.addEventListener('click', () => openReader(c.id));
      frag.appendChild(card);
    });
    el.historyGrid.appendChild(frag);
  }

  function clampPage(v) {
    if (!state.reader.pageCountKnown) {
      return Math.max(0, Number(v) || 0);
    }
    const total = Math.max(1, state.reader.totalPages);
    return Math.max(0, Math.min(Number(v) || 0, total - 1));
  }

  function syncReaderSettingsToControls() {
    const s = state.reader.settings;
    if (el.settingsMode) el.settingsMode.value = s.mode;
    if (el.settingsFit) el.settingsFit.value = s.fit;
    if (el.settingsEnhance) el.settingsEnhance.value = normalizeEnhanceMode(s.enhance);
    if (el.settingsSpreadDirection) el.settingsSpreadDirection.value = s.spreadDirection;
    if (el.settingsMobileCompact) el.settingsMobileCompact.checked = s.mobileCompact;
    syncReaderQuickButtons();
  }

  function syncReaderQuickButtons() {
    // 精简阅读栏后不再维护顶部快捷模式/方向按钮。
  }

  function getReaderStickyOffset() {
    if (!el.readerTopbar) return 12;
    const rect = el.readerTopbar.getBoundingClientRect();
    if (rect.height <= 0) return 12;
    return Math.round(rect.height + 16);
  }

  function scrollToReaderItem(target, behavior) {
    if (!target) return;
    const y = Math.max(0, window.scrollY + target.getBoundingClientRect().top - getReaderStickyOffset());
    window.scrollTo({ top: y, behavior: behavior || 'auto' });
  }

  function syncPageControls() {
    const total = state.reader.pageCountKnown
      ? Math.max(1, state.reader.totalPages)
      : Math.max(1, state.reader.currentPage + 2);
    if (el.pageSlider) {
      el.pageSlider.min = '1';
      el.pageSlider.max = String(total);
      el.pageSlider.value = String(state.reader.currentPage + 1);
      el.pageSlider.disabled = !state.reader.pageCountKnown;
    }
    updateReaderProgressText();
  }

  async function loadReader() {
    if (!readerComicID) return;
    nextReaderRenderEpoch();
    await destroyReaderPDF();
    let detail;
    try {
      detail = await fetchJSON(`api/comics/${readerComicID}`);
    } catch (err) {
      if (err?.status === 404) {
        alert('当前漫画已不存在或已重新扫描，请从书库重新打开。');
        location.href = appURL('./');
        return;
      }
      throw err;
    }
    if (!detail?.comic) return;

    state.reader.comic = detail.comic;
    const format = String(detail.comic.format || '').toLowerCase();
    const isArchive = format === 'zip';
    const originalPageCount = Math.max(0, Number(detail.comic.page_count || 0));
    const knownPageCount = isArchive ? originalPageCount > 1 : originalPageCount > 0;
    state.reader.pageCountKnown = knownPageCount;
    state.reader.totalPages = knownPageCount ? originalPageCount : Math.max(1, originalPageCount);
    state.reader.currentPage = 0;
    state.reader.lastSavedPage = -1;
    state.reader.renderedPages.clear();
    clearReaderImageCache();
    bindStaticReaderPageSlots();

    if (el.readerTitle) el.readerTitle.textContent = detail.comic.title;
    if (el.readerSubTitle) {
      el.readerSubTitle.textContent = state.reader.pageCountKnown ? `${state.reader.totalPages} 页` : '页数统计中';
    }

    if (SINGLE_PAGE_READER && state.reader.settings.mode !== 'paged') {
      state.reader.settings.mode = 'paged';
      saveSettings();
    }

    const progressParams = new URLSearchParams();
    if (detail.comic.file_name_key) {
      progressParams.set('comic_file_name_key', String(detail.comic.file_name_key));
    }
    const progressURL = progressParams.size > 0
      ? `api/comics/${readerComicID}/progress?${progressParams.toString()}`
      : `api/comics/${readerComicID}/progress`;
    const progress = await fetchJSON(progressURL);
    if (progress?.progress) {
      state.reader.currentPage = clampPage(progress.progress.last_page || 0);
      if (!state.reader.pageCountKnown) {
        const pTotal = Math.max(0, Number(progress.progress.total_pages || 0));
        if (pTotal > 1) {
          state.reader.pageCountKnown = true;
          state.reader.totalPages = pTotal;
          state.reader.comic.page_count = pTotal;
        } else {
          state.reader.totalPages = Math.max(state.reader.totalPages, state.reader.currentPage + 1);
        }
      }
    }

    syncReaderSettingsToControls();
    syncPageControls();
    updateFullscreenButton();
    syncReaderFavoriteButton();
    renderReaderByMode(false);

    if (!state.reader.pageCountKnown) {
      void refreshReaderPageCountEventually(readerComicID);
    }
  }

  async function refreshReaderPageCountEventually(comicID) {
    if (!comicID || view !== 'reader') return;
    const waits = [300, 700, 1200, 1800, 2600, 3600];
    for (const ms of waits) {
      await sleep(ms);
      if (view !== 'reader') return;
      try {
        const latest = await fetchJSON(`api/comics/${comicID}`);
        const pc = Math.max(0, Number(latest?.comic?.page_count || 0));
        const latestFormat = String(latest?.comic?.format || '').toLowerCase();
        const latestArchive = latestFormat === 'zip';
        const reliable = latestArchive ? pc > 1 : pc > 0;
        if (reliable) {
          state.reader.pageCountKnown = true;
          state.reader.totalPages = pc;
          if (state.reader.comic) state.reader.comic.page_count = pc;
          if (el.readerSubTitle) el.readerSubTitle.textContent = `${pc} 页`;
          state.reader.currentPage = clampPage(state.reader.currentPage);
          syncPageControls();
          return;
        }
      } catch (_) {}
    }
  }

  function requestWidthBucket() {
    const enhance = getCurrentEnhanceMode();
    if (enhance === ENHANCE_MODE_OFF) return 64;
    if (enhance === ENHANCE_MODE_LEVEL2) return 32;
    return 48;
  }

  function normalizeRequestWidth(rawWidth) {
    const bucket = requestWidthBucket();
    const maxWidth = enhanceRenderProfile(getCurrentEnhanceMode()).requestedWidthMax;
    const safe = Math.max(640, Math.min(maxWidth, Math.round(rawWidth || 0)));
    return Math.max(640, Math.min(maxWidth, Math.ceil(safe / bucket) * bucket));
  }

  function computeDisplayTargetWidth(slot = null) {
    const viewportW = Math.max(320, document.documentElement.clientWidth - 30);
    let baseWidth = Math.max(320, el.readerStage?.clientWidth || window.innerWidth || 1200);
    if (slot && slot.clientWidth > 0) {
      baseWidth = slot.clientWidth;
    } else if (state.reader.settings.mode === 'paged' && state.reader.spread === 'double') {
      baseWidth = Math.max(220, Math.floor((baseWidth - 44) / 2));
    }
    if (isReaderFullscreenActive()) {
      baseWidth = Math.max(baseWidth, window.innerWidth || baseWidth);
    }
    return Math.max(320, Math.min(baseWidth, viewportW));
  }

  function computeRequestedWidth(slot = null) {
    const profile = enhanceRenderProfile(getCurrentEnhanceMode());
    const fullscreenBoost = isReaderFullscreenActive() ? 1.12 : 1;
    const bandwidthScale = getNetworkScaleFactor();
    const dpr = Math.max(1, Math.min(profile.dprMax || 1.6, window.devicePixelRatio || 1));
    const raw = computeDisplayTargetWidth(slot) * profile.requestedWidthScale * dpr * fullscreenBoost * bandwidthScale;
    return normalizeRequestWidth(raw);
  }

  function pageURL(index, reqWidth = computeRequestedWidth()) {
    const params = new URLSearchParams();
    params.set('enhance', getCurrentEnhanceMode());
    params.set('w', String(reqWidth));
    params.set('rv', READER_RENDER_REVISION);
    return apiURL(`api/comics/${state.reader.comic.id}/pages/${index}?${params.toString()}`);
  }

  function pageURLWithFallback(index, reqWidth = computeRequestedWidth()) {
    const primary = pageURL(index, reqWidth);
    const fallbackWidth = normalizeRequestWidth(reqWidth * 0.82);
    const params = new URLSearchParams();
    params.set('enhance', getCurrentEnhanceMode());
    params.set('w', String(Math.min(fallbackWidth, reqWidth)));
    params.set('rv', READER_RENDER_REVISION);
    const fallback = apiURL(`api/comics/${state.reader.comic.id}/pages/${index}?${params.toString()}`);
    return { primary, fallback };
  }

  function getImage(index, requestWidth = computeRequestedWidth()) {
    const key = imageCacheKey(index, requestWidth);
    if (state.imageCache.has(key)) return state.imageCache.get(key);
    const p = (async () => {
      const urls = pageURLWithFallback(index, requestWidth);
      const isPDF = isPDFComic();

      if (!isPDF) {
        return new Promise((resolve, reject) => {
          const img = new Image();
          img.decoding = 'async';
          let triedFallback = false;
          const cleanup = () => {
            img.onload = null;
            img.onerror = null;
          };
          img.onload = () => {
            cleanup();
            resolve(img);
          };
          img.onerror = () => {
            if (!triedFallback) {
              triedFallback = true;
              img.src = urls.fallback;
              return;
            }
            cleanup();
            reject(new Error('page image load failed'));
          };
          img.src = urls.primary;
        });
      }

      let serverImage = null;
      try {
        serverImage = await fetchReaderImage(urls.primary);
        if (serverImage.dataset.pageFallback !== '1') return serverImage;
      } catch (_) {}

      if (!serverImage || serverImage.dataset.pageFallback === '1') {
        try {
          const fallbackImage = await fetchReaderImage(urls.fallback);
          if (fallbackImage.dataset.pageFallback !== '1') return fallbackImage;
          serverImage = fallbackImage;
        } catch (_) {}
      }

      if (serverImage) return serverImage;
      throw new Error('pdf page render failed');
    })();

    const wrapped = p.catch((err) => {
      state.imageCache.delete(key);
      throw err;
    });
    state.imageCache.set(key, wrapped);
    return wrapped;
  }

  function createReaderPageSlot(index) {
    const slot = document.createElement('div');
    slot.className = 'reader-page-slot';

    const img = document.createElement('img');
    img.className = 'reader-page-media';
    img.alt = '漫画页面';
    img.decoding = 'async';
    img.draggable = false;
    img.hidden = true;
    if (Number.isInteger(index) && index >= 0) {
      img.loading = 'lazy';
      img.dataset.index = String(index);
    }
    bindReaderPageImageState(img, slot);
    slot.appendChild(img);

    const error = document.createElement('div');
    error.className = 'reader-page-error';
    error.hidden = true;
    error.setAttribute('aria-live', 'polite');
    slot.appendChild(error);
    return slot;
  }

  function bindReaderPageImageState(img, slot) {
    if (!img || img.dataset.boundState === '1') return;
    img.dataset.boundState = '1';
    img.addEventListener('load', () => {
      if (!isActiveReaderPageSlotRender(img, slot)) return;
      if (slot) slot.hidden = false;
      img.hidden = false;
      img.dataset.state = 'ready';
      const error = slot?.querySelector('.reader-page-error') || null;
      if (error) {
        error.hidden = true;
        error.textContent = '';
      }
    });
    img.addEventListener('error', () => {
      if (!isActiveReaderPageSlotRender(img, slot)) return;
      img.dataset.state = 'error';
      hideReaderPageSlot(slot);
    });
  }

  function bindStaticReaderPageSlots() {
    const slots = [el.pagedSlotLeft, el.pagedSlotRight];
    slots.forEach((slot) => {
      if (!slot) return;
      const img = slot.querySelector('.reader-page-media');
      if (img) bindReaderPageImageState(img, slot);
    });
  }

  function getReaderPageSlotParts(slot) {
    if (!slot) return { img: null, error: null };
    return {
      img: slot.querySelector('.reader-page-media'),
      error: slot.querySelector('.reader-page-error')
    };
  }

  function hideReaderPageSlot(slot) {
    if (!slot) return;
    slot.hidden = true;
    slot.dataset.renderToken = '';
    slot.dataset.pageIndex = '';
    const { img, error } = getReaderPageSlotParts(slot);
    if (img) {
      img.hidden = true;
      img.dataset.state = 'idle';
      img.dataset.renderToken = '';
      img.dataset.pageIndex = '';
      img.dataset.pageFallback = '0';
      img.style.width = '';
      img.style.height = '';
      img.style.maxWidth = '';
      img.style.maxHeight = '';
      img.style.minWidth = '';
      img.style.minHeight = '';
      img.style.imageRendering = '';
      img.removeAttribute('src');
    }
    if (error) {
      error.hidden = true;
      error.textContent = '';
      error.style.width = '';
      error.style.height = '';
    }
  }

  function nextReaderSlotRenderToken(slot, pageIndex) {
    state.readerSlotRenderSeq += 1;
    const token = `${state.renderEpoch}:${pageIndex}:${state.readerSlotRenderSeq}`;
    if (slot) {
      slot.dataset.renderToken = token;
      slot.dataset.pageIndex = String(pageIndex);
    }
    return token;
  }

  function isActiveReaderPageSlotRender(img, slot) {
    if (!img || !slot) return false;
    const token = img.dataset.renderToken || '';
    return !!token && token === (slot.dataset.renderToken || '') && img.dataset.state === 'loading';
  }

  function createStaleRenderError() {
    const err = new Error('stale reader render');
    err.staleRender = true;
    return err;
  }

  async function safeDrawPage(pageIndex, slot, expectedEpoch = state.renderEpoch) {
    try {
      await drawPageToCanvas(pageIndex, slot, expectedEpoch);
      return true;
    } catch (err) {
      if (err && err.staleRender) {
        return true;
      }
      if (expectedEpoch === state.renderEpoch) {
        hideReaderPageSlot(slot);
      }
      return false;
    }
  }

  function pumpScrollRenderQueue() {
    while (state.scrollRenderInFlight < SCROLL_RENDER_MAX_CONCURRENT && state.scrollRenderQueue.length > 0) {
      const task = state.scrollRenderQueue.shift();
      if (!task) continue;
      state.scrollRenderPending.delete(task.index);
      if (state.reader.renderedPages.has(task.index)) {
        continue;
      }
      state.scrollRenderInFlight += 1;
      Promise.resolve()
        .then(async () => {
          state.reader.renderedPages.add(task.index);
          const ok = await safeDrawPage(task.index, task.slot);
          if (!ok) {
            state.reader.renderedPages.delete(task.index);
          }
        })
        .finally(() => {
          state.scrollRenderInFlight = Math.max(0, state.scrollRenderInFlight - 1);
          pumpScrollRenderQueue();
        });
    }
  }

  function enqueueScrollRender(index, slot) {
    if (!slot) return;
    if (state.reader.renderedPages.has(index) || state.scrollRenderPending.has(index)) return;
    state.scrollRenderPending.add(index);
    state.scrollRenderQueue.push({ index, slot });
    pumpScrollRenderQueue();
  }

  function detectDoubleSpreadCandidates(imgA, imgB) {
    const aPortrait = imgA.naturalHeight >= imgA.naturalWidth * 1.08;
    const bPortrait = imgB.naturalHeight >= imgB.naturalWidth * 1.08;
    return aPortrait && bPortrait;
  }

  async function decideSpread(pageIndex) {
    if (SINGLE_PAGE_READER) return 'single';
    if (state.reader.settings.mode !== 'paged') return 'single';
    if (isReaderFullscreenActive()) return 'single';
    if (pageIndex <= 0) return 'single';
    if (pageIndex >= state.reader.totalPages - 1) return 'single';

    const stageW = el.readerStage.clientWidth;
    const stageH = el.readerStage.clientHeight;
    if (stageW < 1080 || stageW < stageH * 1.2) return 'single';

    const rightIndex = pageIndex + 1;
    if (rightIndex >= state.reader.totalPages) return 'single';

    try {
      const [imgA, imgB] = await Promise.all([getImage(pageIndex), getImage(rightIndex)]);
      // Landscape pages are treated as single page.
      const aLandscape = imgA.naturalWidth > imgA.naturalHeight * 1.02;
      const bLandscape = imgB.naturalWidth > imgB.naturalHeight * 1.02;
      if (aLandscape || bLandscape) return 'single';
      return detectDoubleSpreadCandidates(imgA, imgB) ? 'double' : 'single';
    } catch (_) {
      return 'single';
    }
  }

  async function drawPageToCanvas(pageIndex, slot, expectedEpoch = state.renderEpoch) {
    if (!slot) return;
    if (pageIndex < 0 || (state.reader.pageCountKnown && pageIndex >= state.reader.totalPages)) {
      hideReaderPageSlot(slot);
      return;
    }
    if (expectedEpoch !== state.renderEpoch) {
      throw createStaleRenderError();
    }

    let requestedWidth = computeRequestedWidth(slot);
    let img = await getImage(pageIndex, requestedWidth);
    if (expectedEpoch !== state.renderEpoch) {
      throw createStaleRenderError();
    }
    const fit = state.reader.settings.mode === 'scroll' ? 'width' : state.reader.settings.fit;
    const { img: targetImg, error } = getReaderPageSlotParts(slot);
    if (!targetImg || !error) return;

    const viewportW = Math.max(320, document.documentElement.clientWidth - 30);
    const inFullscreen = isReaderFullscreenActive();
    let maxW = Math.max(320, Math.min((slot.clientWidth || viewportW) - 10, viewportW));
    let maxH = Math.max(320, (slot.clientHeight || maxW) - 10);

    if (state.reader.settings.mode === 'paged' && state.reader.spread === 'double') {
      maxW = Math.max(220, Math.min(Math.floor((el.readerStage.clientWidth - 44) / 2), Math.floor((viewportW - 20) / 2)));
    }
    if (inFullscreen) {
      maxW = Math.max(320, window.innerWidth - 2);
      maxH = Math.max(320, window.innerHeight - 2);
    }

    const iw = img.naturalWidth || 800;
    const ih = img.naturalHeight || 1200;

    let scale;
    if (inFullscreen) {
      scale = Math.min(maxW / iw, maxH / ih);
    } else {
      const widthScale = maxW / iw;
      const heightScale = maxH / ih;
      if (fit === 'height') {
        scale = heightScale;
      } else {
        scale = widthScale;
        if ((widthScale * ih) > maxH * 1.01) {
          scale = Math.min(widthScale, heightScale);
        }
      }
      scale = Math.max(0.22, Math.min(scale, 2.5));
    }

    let finalW = Math.round(iw * scale);
    let finalH = Math.round(ih * scale);
    if (finalW > maxW) {
      scale = maxW / iw;
      finalW = Math.round(iw * scale);
      finalH = Math.round(ih * scale);
    }

    finalW = Math.max(1, finalW);
    finalH = Math.max(1, finalH);

    const nextSrc = img.currentSrc || img.src;
    if (!nextSrc) {
      throw new Error('empty image source');
    }
    if (expectedEpoch !== state.renderEpoch) {
      throw createStaleRenderError();
    }
    const nextToken = nextReaderSlotRenderToken(slot, pageIndex);
    const currentSrc = targetImg.currentSrc || targetImg.src || '';
    const canReuseReadyImage = currentSrc === nextSrc
      && targetImg.dataset.state === 'ready'
      && targetImg.dataset.pageIndex === String(pageIndex);

    slot.hidden = true;
    error.hidden = true;
    error.textContent = '';
    error.style.width = '';
    error.style.height = '';

    targetImg.hidden = true;
    targetImg.style.maxWidth = 'none';
    targetImg.style.maxHeight = 'none';
    targetImg.style.minWidth = '0';
    targetImg.style.minHeight = '0';
    targetImg.style.width = `${finalW}px`;
    targetImg.style.height = `${finalH}px`;
    targetImg.style.imageRendering = 'auto';
    targetImg.dataset.pageIndex = String(pageIndex);
    targetImg.dataset.pageFallback = img.dataset.pageFallback || '0';
    targetImg.dataset.renderToken = nextToken;
    targetImg.dataset.state = canReuseReadyImage ? 'ready' : 'loading';

    if (canReuseReadyImage) {
      targetImg.hidden = false;
      slot.hidden = false;
      return;
    }

    if (currentSrc === nextSrc) {
      targetImg.removeAttribute('src');
    }
    targetImg.src = nextSrc;
  }

  async function renderPagedView(pageIndex) {
    const renderEpoch = nextReaderRenderEpoch();
    if (state.reader.totalPages <= 0) {
      state.reader.spread = 'single';
      if (el.readerStage) el.readerStage.dataset.spread = 'single';
      hideReaderPageSlot(el.pagedSlotLeft);
      hideReaderPageSlot(el.pagedSlotRight);
      updateReaderProgressText();
      return;
    }

    let page = clampPage(pageIndex);
    state.reader.currentPage = page;
    if (el.readerStage) el.readerStage.dataset.spread = 'single';
    hideReaderPageSlot(el.pagedSlotRight);
    if (!state.reader.pageCountKnown) {
      state.reader.totalPages = Math.max(state.reader.totalPages, page + 1);
    }

    state.reader.spread = await decideSpread(page);
    if (renderEpoch !== state.renderEpoch) return;
    el.readerStage.dataset.spread = state.reader.spread;

    let mainPageOK = true;
    if (state.reader.spread === 'double') {
      if (el.pagedSlotRight) el.pagedSlotRight.hidden = false;
      if (isRTL()) {
        // Right-open: current page at right side, next page at left side.
        await safeDrawPage(page + 1, el.pagedSlotLeft, renderEpoch);
        if (renderEpoch !== state.renderEpoch) return;
        mainPageOK = await safeDrawPage(page, el.pagedSlotRight, renderEpoch);
      } else {
        mainPageOK = await safeDrawPage(page, el.pagedSlotLeft, renderEpoch);
        if (renderEpoch !== state.renderEpoch) return;
        await safeDrawPage(page + 1, el.pagedSlotRight, renderEpoch);
      }
    } else {
      mainPageOK = await safeDrawPage(page, el.pagedSlotLeft, renderEpoch);
      hideReaderPageSlot(el.pagedSlotRight);
    }
    if (renderEpoch !== state.renderEpoch) return;

    if (!mainPageOK && !state.reader.pageCountKnown && page > 0) {
      const discovered = Math.max(1, page);
      state.reader.pageCountKnown = true;
      state.reader.totalPages = discovered;
      if (state.reader.comic) state.reader.comic.page_count = discovered;
      if (el.readerSubTitle) el.readerSubTitle.textContent = `${discovered} 页`;
      const rollback = clampPage(page - 1);
      syncPageControls();
      if (rollback !== page) {
        await renderPagedView(rollback);
      }
      return;
    }

    updateReaderProgressText();
    scheduleSaveProgress();
  }

  function renderScrollScaffold(preservePosition) {
    const renderEpoch = state.renderEpoch;
    state.reader.renderedPages.clear();
    resetScrollRenderQueue();
    el.readerPages.innerHTML = '';
    if (state.reader.totalPages <= 0) {
      const item = document.createElement('article');
      item.className = 'scroll-item';
      const slot = createReaderPageSlot(-1);
      item.appendChild(slot);
      el.readerPages.appendChild(item);
      hideReaderPageSlot(slot);
      return;
    }

    for (let i = 0; i < state.reader.totalPages; i += 1) {
      const item = document.createElement('article');
      item.className = 'scroll-item';
      item.dataset.index = String(i);
      const slot = createReaderPageSlot(i);
      slot.dataset.index = String(i);
      item.appendChild(slot);
      el.readerPages.appendChild(item);
    }

    if (state.scrollObserver) state.scrollObserver.disconnect();
    state.scrollObserver = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (renderEpoch !== state.renderEpoch) return;
        const item = entry.target;
        const index = Number(item.dataset.index || 0);
        if (entry.isIntersecting) {
          const slot = item.querySelector('.reader-page-slot');
          if (slot) {
            enqueueScrollRender(index, slot);
          }
        }
      });
    }, { root: null, rootMargin: '900px 0px 900px 0px' });

    el.readerPages.querySelectorAll('.scroll-item').forEach(n => state.scrollObserver.observe(n));

    const target = el.readerPages.querySelector(`.scroll-item[data-index="${state.reader.currentPage}"]`);
    if (target && !preservePosition) {
      setTimeout(() => scrollToReaderItem(target, 'auto'), 50);
    }

    if (!state.windowScrollBound) {
      window.addEventListener('scroll', onWindowScroll, { passive: true });
      state.windowScrollBound = true;
    }
  }

  function onWindowScroll() {
    if (view !== 'reader') return;
    if (state.reader.settings.mode !== 'scroll') return;
    if (state.scrollRAF) return;
    state.scrollRAF = requestAnimationFrame(() => {
      state.scrollRAF = 0;
      updateCurrentPageByScroll();
    });
  }

  function updateCurrentPageByScroll() {
    const items = el.readerPages.querySelectorAll('.scroll-item');
    if (!items.length) return;

    const anchor = Math.max(getReaderStickyOffset() + 20, Math.round(window.innerHeight * 0.4));
    let best = state.reader.currentPage;
    let minDist = Infinity;

    items.forEach(item => {
      const rect = item.getBoundingClientRect();
      let d = 0;
      if (anchor < rect.top) {
        d = rect.top - anchor;
      } else if (anchor > rect.bottom) {
        d = anchor - rect.bottom;
      } else {
        d = 0;
      }
      if (d < minDist) {
        minDist = d;
        best = Number(item.dataset.index || 0);
      }
    });

    if (best !== state.reader.currentPage) {
      state.reader.currentPage = best;
      updateReaderProgressText();
      scheduleSaveProgress();
    }
  }

  function renderReaderByMode(preservePosition) {
    if (!state.reader.comic) return;
    nextReaderRenderEpoch();
    if (SINGLE_PAGE_READER && state.reader.settings.mode !== 'paged') {
      state.reader.settings.mode = 'paged';
      saveSettings();
    }
    if (el.readerStage) {
      el.readerStage.dataset.fit = state.reader.settings.mode === 'scroll' ? 'width' : state.reader.settings.fit;
    }
    syncReaderQuickButtons();

    if (state.reader.settings.mode === 'scroll') {
      hideReaderPageSlot(el.pagedSlotLeft);
      hideReaderPageSlot(el.pagedSlotRight);
      el.readerPagedWrap.hidden = true;
      el.readerScrollWrap.hidden = false;
      el.readerStage.dataset.mode = 'scroll';
      el.readerStage.dataset.spread = 'single';
      if (preservePosition && el.readerPages && el.readerPages.childElementCount === state.reader.totalPages) {
        updateCurrentPageByScroll();
        return;
      }
      renderScrollScaffold(preservePosition);
    } else {
      if (state.scrollObserver) {
        state.scrollObserver.disconnect();
        state.scrollObserver = null;
      }
      resetScrollRenderQueue();
      el.readerScrollWrap.hidden = true;
      el.readerPagedWrap.hidden = false;
      el.readerStage.dataset.mode = 'paged';
      renderPagedView(state.reader.currentPage);
    }
  }

  function updateReaderProgressText() {
    if (state.reader.totalPages <= 0) {
      if (el.readerProgressText) {
        el.readerProgressText.textContent = '当前漫画暂无可读页面';
      }
      if (el.pageSlider) {
        el.pageSlider.min = '1';
        el.pageSlider.max = '1';
        el.pageSlider.value = '1';
      }
      return;
    }

    const cur = state.reader.currentPage + 1;
    const total = state.reader.pageCountKnown ? Math.max(1, state.reader.totalPages) : '?';
    if (el.readerProgressText) {
      const spreadText = state.reader.settings.mode === 'paged' ? (state.reader.spread === 'double' ? '双页' : '单页') : '滚动';
      el.readerProgressText.textContent = `当前第 ${cur}/${total} 页 · ${spreadText}`;
    }
    if (el.pageSlider) el.pageSlider.value = String(cur);
  }

  function scheduleSaveProgress() {
    if (!state.reader.comic) return;
    const page = state.reader.currentPage;
    if (state.reader.lastSavedPage === page) return;
    if (state.reader.saveTimer) clearTimeout(state.reader.saveTimer);

    state.reader.saveTimer = setTimeout(async () => {
      try {
        const totalPages = state.reader.pageCountKnown
          ? state.reader.totalPages
          : Math.max(state.reader.totalPages, page + 1);
        await fetchJSON(`api/comics/${state.reader.comic.id}/progress`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            last_page: page,
            total_pages: totalPages,
            comic_file_name_key: String(state.reader.comic.file_name_key || '')
          })
        });
        state.reader.lastSavedPage = page;
      } catch (_) {}
    }, 260);
  }

  function jumpToPage(page1Based) {
    if (state.reader.totalPages <= 0) return;
    const page = clampPage((Number(page1Based) || 1) - 1);
    state.reader.currentPage = page;
    if (state.reader.settings.mode === 'scroll') {
      const target = el.readerPages.querySelector(`.scroll-item[data-index="${page}"]`);
      if (target) scrollToReaderItem(target, 'smooth');
      updateReaderProgressText();
      scheduleSaveProgress();
      return;
    }
    renderPagedView(page);
  }

  function pagedStep() {
    return 1;
  }

  function movePages(delta) {
    const next = clampPage(state.reader.currentPage + delta);
    jumpToPage(next + 1);
  }

  function logicalDeltaToRaw(delta) {
    return delta;
  }

  function isMobileViewport() {
    return window.matchMedia('(max-width: 900px)').matches || window.matchMedia('(pointer: coarse)').matches;
  }

  function isReaderFullscreenActive() {
    return !!document.fullscreenElement || !!state.reader.pseudoFullscreen;
  }

  function pushReaderFullscreenHistoryState() {
    if (state.reader.fullscreenHistoryPushed) return;
    try {
      history.pushState({ readerFullscreen: true }, '', location.href);
      state.reader.fullscreenHistoryPushed = true;
    } catch (_) {}
  }

  function clearReaderFullscreenHistoryState() {
    state.reader.fullscreenHistoryPushed = false;
  }

  function popReaderFullscreenHistoryIfNeeded() {
    if (!state.reader.fullscreenHistoryPushed) return;
    if (history.state && history.state.readerFullscreen) {
      history.back();
      return;
    }
    clearReaderFullscreenHistoryState();
  }

  function reconcileReaderFullscreenHistory() {
    if (!state.reader.fullscreenHistoryPushed) return;
    if (history.state && history.state.readerFullscreen) return;
    clearReaderFullscreenHistoryState();
  }

  async function lockPortraitOnMobileIfPossible() {
    if (!isMobileViewport()) return;
    const orientation = window.screen && window.screen.orientation;
    if (!orientation || typeof orientation.lock !== 'function') return;
    try {
      await orientation.lock('portrait');
    } catch (_) {}
  }

  function unlockOrientationIfPossible() {
    const orientation = window.screen && window.screen.orientation;
    if (!orientation || typeof orientation.unlock !== 'function') return;
    try {
      orientation.unlock();
    } catch (_) {}
  }

  function bindSwipeGesture() {
    if (!el.readerStage) return;

    el.readerStage.addEventListener('pointerdown', (e) => {
      if (view !== 'reader') return;
      if (state.reader.settings.mode !== 'paged') return;
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      if (e.target.closest && e.target.closest('.reader-side')) return;
      swipeState = { x: e.clientX, y: e.clientY, t: performance.now() };
    });

    el.readerStage.addEventListener('pointerup', (e) => {
      if (!swipeState) return;
      const dx = e.clientX - swipeState.x;
      const dy = e.clientY - swipeState.y;
      const dt = performance.now() - swipeState.t;
      swipeState = null;

      if (Math.abs(dx) < 56) return;
      if (Math.abs(dx) < Math.abs(dy) * 1.2) return;
      if (dt > 800) return;

      const step = pagedStep();
      if (dx < 0) {
        movePages(logicalDeltaToRaw(step));
      } else {
        movePages(logicalDeltaToRaw(-step));
      }
    });

    el.readerStage.addEventListener('pointercancel', () => {
      swipeState = null;
    });
  }

  async function toggleFullscreen() {
    if (!el.readerStage) return;
    if (isMobileViewport()) {
      state.reader.pseudoFullscreen = !state.reader.pseudoFullscreen;
      document.body.classList.toggle('fullscreen-reading', isReaderFullscreenActive());
      if (state.reader.pseudoFullscreen) {
        await lockPortraitOnMobileIfPossible();
        pushReaderFullscreenHistoryState();
      } else {
        unlockOrientationIfPossible();
        popReaderFullscreenHistoryIfNeeded();
      }
    } else if (document.fullscreenElement) {
      unlockOrientationIfPossible();
      await document.exitFullscreen();
      return;
    } else {
      await el.readerStage.requestFullscreen();
      pushReaderFullscreenHistoryState();
    }
    updateFullscreenButton();
    renderReaderByMode(true);
  }

  function updateFullscreenButton() {
    if (!el.readerFullscreenBtn) return;
    el.readerFullscreenBtn.textContent = isReaderFullscreenActive() ? '退出全屏' : '全屏';
  }

  function bindReaderControls() {
    if (el.pageSlider) el.pageSlider.addEventListener('input', () => jumpToPage(el.pageSlider.value));

    if (el.readerFavoriteBtn) {
      el.readerFavoriteBtn.addEventListener('click', async () => {
        if (!state.reader.comic?.id) return;
        const nextFavorite = !Boolean(state.reader.comic.is_favorite);
        el.readerFavoriteBtn.disabled = true;
        try {
          await setComicFavorite(state.reader.comic.id, nextFavorite, state.reader.comic.file_name_key);
          state.reader.comic.is_favorite = nextFavorite;
          syncReaderFavoriteButton();
        } catch (err) {
          alert('更新收藏失败: ' + (err?.message || String(err)));
        } finally {
          el.readerFavoriteBtn.disabled = false;
        }
      });
    }

    if (el.readerPrevBtn) el.readerPrevBtn.addEventListener('click', () => movePages(-1));
    if (el.readerNextBtn) {
      el.readerNextBtn.addEventListener('click', () => {
        movePages(1);
      });
    }
    if (el.readerFirstBtn) el.readerFirstBtn.addEventListener('click', () => jumpToPage(1));
    if (el.readerLastBtn) el.readerLastBtn.addEventListener('click', () => jumpToPage(state.reader.totalPages));

    if (el.readerFullscreenBtn) el.readerFullscreenBtn.addEventListener('click', toggleFullscreen);
    if (el.closeReaderBtn) el.closeReaderBtn.addEventListener('click', closeReader);

    if (el.readerPagedWrap) {
      el.readerPagedWrap.addEventListener('click', (e) => {
        if (view !== 'reader') return;
        if (state.reader.settings.mode !== 'paged') return;
        if (e.target.closest && e.target.closest('.reader-topbar')) return;
        const rect = el.readerPagedWrap.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const step = pagedStep();
        if (x < rect.width * 0.4) {
          movePages(logicalDeltaToRaw(-step));
        } else if (x > rect.width * 0.6) {
          movePages(logicalDeltaToRaw(step));
        }
      });
    }

    window.addEventListener('keydown', (e) => {
      if (view !== 'reader') return;
      const key = String(e?.key || '');
      if (e.key === 'ArrowLeft') {
        e.preventDefault();
        movePages(logicalDeltaToRaw(-pagedStep()));
      }
      if (e.key === 'ArrowRight') {
        e.preventDefault();
        movePages(logicalDeltaToRaw(pagedStep()));
      }
      if (key.toLowerCase() === 'f') {
        e.preventDefault();
        toggleFullscreen();
      }
    });

    document.addEventListener('fullscreenchange', () => {
      const inFullscreen = !!document.fullscreenElement;
      if (inFullscreen) {
        lockPortraitOnMobileIfPossible();
      } else {
        unlockOrientationIfPossible();
      }
      updateFullscreenButton();
      document.body.classList.toggle('fullscreen-reading', isReaderFullscreenActive());
      if (!isReaderFullscreenActive() && state.reader.fullscreenHistoryPushed) {
        reconcileReaderFullscreenHistory();
        if (state.reader.fullscreenHistoryPushed) {
          popReaderFullscreenHistoryIfNeeded();
          return;
        }
      }
      renderReaderByMode(true);
    });

    window.addEventListener('popstate', () => {
      if (!state.reader.fullscreenHistoryPushed) return;
      if (state.reader.pseudoFullscreen) {
        state.reader.pseudoFullscreen = false;
        unlockOrientationIfPossible();
        document.body.classList.remove('fullscreen-reading');
        clearReaderFullscreenHistoryState();
        updateFullscreenButton();
        renderReaderByMode(true);
        return;
      }
      if (document.fullscreenElement) {
        unlockOrientationIfPossible();
        document.exitFullscreen().catch(() => {});
        return;
      }
      clearReaderFullscreenHistoryState();
      updateFullscreenButton();
      renderReaderByMode(true);
    });

    window.addEventListener('resize', () => {
      if (view !== 'reader') return;
      if (resizeTimer) clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        resizeTimer = null;
        if (state.reader.settings.mode === 'paged') {
          renderReaderByMode(true);
        } else {
          updateCurrentPageByScroll();
        }
      }, 120);
    });

    bindSwipeGesture();
  }

  function initSettingsPage() {
    syncReaderSettingsToControls();
    syncRuntimeSettingsToControls();
    if (!el.settingsForm) return;

    el.settingsForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      state.reader.settings.mode = el.settingsMode.value === 'scroll' ? 'scroll' : 'paged';
      state.reader.settings.fit = el.settingsFit.value === 'height' ? 'height' : 'width';
      state.reader.settings.enhance = normalizeEnhanceMode(el.settingsEnhance?.value);
      state.reader.settings.spreadDirection = el.settingsSpreadDirection.value === 'ltr' ? 'ltr' : 'rtl';
      state.reader.settings.sharpen = shouldSharpenByEnhance(getCurrentEnhanceMode());
      state.reader.settings.mobileCompact = !!el.settingsMobileCompact.checked;
      try {
        await saveSettingsNow();
      } catch (err) {
        alert('保存设置失败: ' + (err?.message || String(err)));
        return;
      }
      syncReaderSettingsToControls();
      document.body.classList.toggle('mobile-compact', state.reader.settings.mobileCompact);
      alert('设置已保存');
    });

    if (el.passwordForm) {
      el.passwordForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const currentPwd = String(el.currentPassword.value || '').trim();
        const newPwd = String(el.newPassword.value || '').trim();
        const confirmPwd = String(el.confirmPassword.value || '').trim();

        if (!currentPwd) {
          alert('请输入当前密码');
          el.currentPassword.focus();
          return;
        }
        if (!newPwd) {
          alert('请输入新密码');
          el.newPassword.focus();
          return;
        }
        if (newPwd.length < 6) {
          alert('新密码至少需要6个字符');
          el.newPassword.focus();
          return;
        }
        if (newPwd !== confirmPwd) {
          alert('两次输入的新密码不一致');
          el.confirmPassword.focus();
          return;
        }

        try {
          await fetchJSON('api/me/password', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              current_password: currentPwd,
              new_password: newPwd
            })
          });
          alert('密码修改成功');
          el.currentPassword.value = '';
          el.newPassword.value = '';
          el.confirmPassword.value = '';
        } catch (err) {
          alert('密码修改失败: ' + (err?.message || String(err)));
        }
      });
    }

    if (isAdmin && el.runtimeForm && el.runtimeForm.dataset.bound !== '1') {
      el.runtimeForm.dataset.bound = '1';
      el.runtimeForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const memoryLimitMB = Math.floor(Number(el.runtimeMemoryLimitMB?.value || DEFAULT_RUNTIME_SETTINGS.memoryLimitMB));
        const diskCacheMB = Math.floor(Number(el.runtimeDiskCacheMB?.value || DEFAULT_RUNTIME_SETTINGS.diskCacheMB));
        const pagePrefetchPages = Math.floor(Number(el.runtimePagePrefetchPages?.value || DEFAULT_RUNTIME_SETTINGS.pagePrefetchPages));
        const pageLoadParallelism = Math.floor(
          Number(el.runtimePageLoadParallelism?.value || DEFAULT_RUNTIME_SETTINGS.pageLoadParallelism)
        );
        if (!Number.isFinite(memoryLimitMB) || memoryLimitMB < 256 || memoryLimitMB > 32768) {
          alert('内存上限必须在 256-32768 MB');
          el.runtimeMemoryLimitMB?.focus();
          return;
        }
        if (!Number.isFinite(diskCacheMB) || diskCacheMB < 256 || diskCacheMB > 1048576) {
          alert('磁盘缓存上限必须在 256-1048576 MB');
          el.runtimeDiskCacheMB?.focus();
          return;
        }
        if (!Number.isFinite(pagePrefetchPages) || pagePrefetchPages < 1 || pagePrefetchPages > 100) {
          alert('页面预取数量必须在 1-100');
          el.runtimePagePrefetchPages?.focus();
          return;
        }
        if (!Number.isFinite(pageLoadParallelism) || pageLoadParallelism < 1 || pageLoadParallelism > 16) {
          alert('单漫画缓存加载并行页数必须在 1-16');
          el.runtimePageLoadParallelism?.focus();
          return;
        }
        state.runtime = {
          memoryLimitMB,
          diskCacheMB,
          pagePrefetchPages,
          pageLoadParallelism
        };
        try {
          await saveRuntimeSettingsNow();
          alert('运行时设置已保存并生效');
        } catch (err) {
          alert('保存运行时设置失败: ' + (err?.message || String(err)));
        }
      });
    }
  }

  function showSettingsSection(key) {
    const requested = String(key || 'reader');
    const validSections = new Set();
    if (el.settingsModules) {
      el.settingsModules.forEach((section) => {
        if (section.classList.contains('admin-only') && !isAdmin) return;
        const sectionKey = section.getAttribute('data-settings-section');
        if (sectionKey) validSections.add(sectionKey);
      });
    }
    const target = validSections.has(requested) ? requested : 'reader';
    if (el.settingsModules) {
      el.settingsModules.forEach((section) => {
        if (section.classList.contains('admin-only') && !isAdmin) {
          section.hidden = true;
          return;
        }
        const sectionKey = section.getAttribute('data-settings-section');
        section.hidden = sectionKey !== target;
      });
    }
    if (el.settingsIndexButtons) {
      el.settingsIndexButtons.forEach((btn) => {
        btn.classList.toggle('active', btn.getAttribute('data-settings-section') === target);
      });
    }
  }

  function initSettingsIndex() {
    if (!el.settingsIndexButtons) return;
    el.settingsIndexButtons.forEach((btn) => {
      if (btn.dataset.bound === '1') return;
      btn.dataset.bound = '1';
      btn.addEventListener('click', () => {
        const key = btn.getAttribute('data-settings-section') || 'reader';
        showSettingsSection(key);
      });
    });
  }

  function setMountTypeUI() {
    if (!el.mountType) return;
    const t = el.mountType.value;
    document.querySelectorAll('.smb-only').forEach(n => (n.hidden = t !== 'smb'));
    document.querySelectorAll('.webdav-only').forEach(n => (n.hidden = t !== 'webdav'));
  }

  function selectedRootDirectoryPath() {
    if (!el.mountRootDirectorySelect) return '/';
    const selectedID = Number(el.mountRootDirectorySelect.value || 0);
    const found = state.rootDirectories.find(x => Number(x.id) === selectedID);
    return normalizeRootPath(found?.path || '/');
  }

  function joinRootPath(basePath, relPath) {
    const base = normalizeRootPath(basePath || '/');
    const rel = normalizeRootPath(relPath || '/');
    if (rel === '/') return base;
    if (base === '/') return rel;
    return normalizeRootPath(`${base}${rel}`);
  }

  function normalizeRootPath(value) {
    let v = String(value || '').trim();
    if (!v) return '/';
    v = v.replace(/\\/g, '/');
    if (!v.startsWith('/')) v = `/${v}`;
    v = v.replace(/\/{2,}/g, '/');
    if (v.length > 1 && v.endsWith('/')) v = v.slice(0, -1);
    return v || '/';
  }

  function getMountRootPaths() {
    const out = [];
    const seen = new Set();
    (state.mountRootPaths || []).forEach((item) => {
      const normalized = normalizeRootPath(item);
      if (seen.has(normalized)) return;
      seen.add(normalized);
      out.push(normalized);
    });
    return out;
  }

  function renderMountRootPaths() {
    if (!el.mountRootPaths || !el.mountRootPathsEmpty) return;
    const paths = getMountRootPaths();
    state.mountRootPaths = paths;
    el.mountRootPaths.innerHTML = '';
    paths.forEach((p) => {
      const item = document.createElement('div');
      item.className = 'mount-root-chip';

      const pathText = document.createElement('span');
      pathText.textContent = p;
      item.appendChild(pathText);

      const removeBtn = document.createElement('button');
      removeBtn.type = 'button';
      removeBtn.className = 'btn danger';
      removeBtn.textContent = '移除';
      removeBtn.addEventListener('click', () => {
        state.mountRootPaths = paths.filter((x) => x !== p);
        renderMountRootPaths();
      });
      item.appendChild(removeBtn);
      el.mountRootPaths.appendChild(item);
    });

    el.mountRootPathsEmpty.hidden = paths.length > 0;
  }

  function addMountRootPath(path) {
    const next = normalizeRootPath(path);
    const current = getMountRootPaths();
    if (!current.includes(next)) current.push(next);
    state.mountRootPaths = current;
    renderMountRootPaths();
  }

  function renderRootDirectorySelect() {
    if (!el.mountRootDirectorySelect) return;
    const previous = Number(el.mountRootDirectorySelect.value || 0);
    el.mountRootDirectorySelect.innerHTML = '';
    if (!Array.isArray(state.rootDirectories) || state.rootDirectories.length === 0) {
      const opt = document.createElement('option');
      opt.value = '';
      opt.textContent = '请先在“根目录库”里新增';
      el.mountRootDirectorySelect.appendChild(opt);
      return;
    }

    state.rootDirectories.forEach((item) => {
      const opt = document.createElement('option');
      opt.value = String(item.id);
      opt.textContent = `${item.name} · ${normalizeRootPath(item.path || '/')}`;
      el.mountRootDirectorySelect.appendChild(opt);
    });

    const hasPrev = state.rootDirectories.some(x => Number(x.id) === previous);
    el.mountRootDirectorySelect.value = hasPrev ? String(previous) : String(state.rootDirectories[0].id);
  }

  function renderRootDirectoryList() {
    if (!el.rootDirectoryList) return;
    el.rootDirectoryList.innerHTML = '';
    state.rootDirectories.forEach((item) => {
      const li = document.createElement('li');
      const left = document.createElement('span');
      left.textContent = `${item.name} · ${normalizeRootPath(item.path || '/')}`;
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'btn danger';
      del.textContent = '删除';
      del.addEventListener('click', async () => {
        if (!confirm(`确认删除根目录 ${item.name} ?`)) return;
        await fetchJSON(`api/root-directories/${item.id}`, { method: 'DELETE' });
        await loadRootDirectories();
      });
      li.append(left, del);
      el.rootDirectoryList.appendChild(li);
    });
  }

  async function loadRootDirectories() {
    if (!isAdmin) return;
    const data = await fetchJSON('api/root-directories');
    state.rootDirectories = Array.isArray(data?.items) ? data.items : [];
    renderRootDirectoryList();
    renderRootDirectorySelect();
    renderWebPSourceRootOptions();
    renderWebPOutputRootOptions();
  }

  async function submitRootDirectoryForm(e) {
    e.preventDefault();
    const form = new FormData(el.rootDirectoryForm);
    const payload = {
      name: String(form.get('name') || '').trim(),
      path: String(form.get('path') || '').trim()
    };
    await fetchJSON('api/root-directories', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    el.rootDirectoryForm.reset();
    await loadRootDirectories();
  }

  function mountBrowsePayload(path) {
    const form = new FormData(el.mountForm);
    const rootPath = selectedRootDirectoryPath();
    return {
      type: String(form.get('type') || '').trim(),
      path: normalizeRootPath(path),
      root_path: rootPath,
      smb_host: String(form.get('smb_host') || '').trim(),
      smb_port: Number(form.get('smb_port') || 445),
      smb_share: String(form.get('smb_share') || '').trim(),
      smb_user: String(form.get('smb_user') || '').trim(),
      smb_password: String(form.get('smb_password') || ''),
      smb_domain: String(form.get('smb_domain') || '').trim(),
      webdav_url: String(form.get('webdav_url') || '').trim(),
      webdav_user: String(form.get('webdav_user') || '').trim(),
      webdav_password: String(form.get('webdav_password') || '')
    };
  }

  async function loadMountTree(path) {
    if (!el.mountTreeRoot) return;
    const payload = mountBrowsePayload(path);
    const data = await fetchJSON('api/mounts/browse', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    renderMountTree(data);
  }

  function treeNodeKey(path) {
    return normalizeRootPath(path);
  }

  function renderMountTree(data) {
    if (!el.mountTreeRoot) return;
    const currentPath = normalizeRootPath(data?.current_path || '/');
    state.mountTreeExpanded.add(treeNodeKey(currentPath));
    const list = document.createElement('ul');
    list.className = 'mount-tree-list';

    const rootNode = buildMountTreeNode({
      name: currentPath,
      path: currentPath
    }, data?.items || [], true);
    list.appendChild(rootNode);
    el.mountTreeRoot.innerHTML = '';
    el.mountTreeRoot.appendChild(list);
  }

  function buildMountTreeNode(node, children, isRoot) {
    const li = document.createElement('li');
    li.className = 'mount-tree-node';
    li.dataset.path = node.path;

    const row = document.createElement('div');
    row.className = 'mount-tree-row';
    row.setAttribute('role', 'treeitem');
    row.setAttribute('aria-expanded', String(state.mountTreeExpanded.has(treeNodeKey(node.path))));

    const toggleBtn = document.createElement('button');
    toggleBtn.type = 'button';
    toggleBtn.className = 'mount-tree-toggle';
    toggleBtn.textContent = state.mountTreeExpanded.has(treeNodeKey(node.path)) ? '▾' : '▸';
    row.appendChild(toggleBtn);

    const label = document.createElement('button');
    label.type = 'button';
    label.className = 'mount-tree-label';
    label.textContent = isRoot ? `当前节点: ${node.path}` : node.name;
    row.appendChild(label);

    const addBtn = document.createElement('button');
    addBtn.type = 'button';
    addBtn.className = 'btn';
    addBtn.textContent = '添加';
    addBtn.addEventListener('click', () => addMountRootPath(joinRootPath(selectedRootDirectoryPath(), node.path)));
    row.appendChild(addBtn);
    li.appendChild(row);

    const childWrap = document.createElement('ul');
    childWrap.className = 'mount-tree-list';
    childWrap.hidden = !state.mountTreeExpanded.has(treeNodeKey(node.path));
    li.appendChild(childWrap);

    if (Array.isArray(children)) {
      children.forEach((child) => {
        const childNode = buildMountTreeNode({
          name: String(child.name || child.path || '/'),
          path: normalizeRootPath(child.path || '/')
        }, null, false);
        childWrap.appendChild(childNode);
      });
    }

    const expandOrCollapse = async () => {
      const key = treeNodeKey(node.path);
      if (state.mountTreeExpanded.has(key)) {
        state.mountTreeExpanded.delete(key);
        childWrap.hidden = true;
        toggleBtn.textContent = '▸';
        row.setAttribute('aria-expanded', 'false');
        return;
      }

      state.mountTreeExpanded.add(key);
      toggleBtn.textContent = '▾';
      row.setAttribute('aria-expanded', 'true');

      if (childWrap.childElementCount === 0) {
        try {
          const payload = mountBrowsePayload(node.path);
          const data = await fetchJSON('api/mounts/browse', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
          });
          const items = data?.items || [];
          items.forEach((child) => {
            const childNode = buildMountTreeNode({
              name: String(child.name || child.path || '/'),
              path: normalizeRootPath(child.path || '/')
            }, null, false);
            childWrap.appendChild(childNode);
          });
        } catch (err) {
          alert('加载目录失败: ' + (err?.message || String(err)));
          state.mountTreeExpanded.delete(key);
          toggleBtn.textContent = '▸';
          row.setAttribute('aria-expanded', 'false');
          return;
        }
      }
      childWrap.hidden = false;
    };

    toggleBtn.addEventListener('click', () => {
      void expandOrCollapse();
    });
    label.addEventListener('click', async () => {
      if (el.mountBrowsePathInput) el.mountBrowsePathInput.value = normalizeRootPath(node.path);
      await expandOrCollapse();
    });

    return li;
  }

  function renderMountList() {
    if (!el.mountList) return;
    el.mountList.innerHTML = '';
    state.mounts.forEach(m => {
      const li = document.createElement('li');
      const left = document.createElement('span');
      const roots = Array.isArray(m.root_paths) && m.root_paths.length > 0
        ? m.root_paths.map(v => normalizeRootPath(v))
        : [normalizeRootPath(m.root_path || '/')];
      left.textContent = `${m.name} · ${mountTypeName(m.type)} · 根路径 ${roots.length} 个 · ${m.enabled ? '启用' : '停用'}`;
      const del = document.createElement('button');
      del.className = 'btn danger';
      del.textContent = '删除';
      del.addEventListener('click', async () => {
        if (!confirm(`确认删除挂载 ${m.name} ?`)) return;
        await fetchJSON(`api/mounts/${m.id}`, { method: 'DELETE' });
        await loadMounts();
      });
      li.append(left, del);
      el.mountList.appendChild(li);
    });
  }

  async function submitMountForm(e) {
    e.preventDefault();
    const form = new FormData(el.mountForm);
    const rootPaths = getMountRootPaths();
    if (rootPaths.length === 0) {
      alert('请先从根目录库选择起点，并在目录树中添加至少一个挂载路径');
      return;
    }
    const payload = {
      name: String(form.get('name') || '').trim(),
      type: String(form.get('type') || '').trim(),
      root_path: rootPaths[0] || '/',
      root_paths: rootPaths,
      smb_host: String(form.get('smb_host') || '').trim(),
      smb_port: Number(form.get('smb_port') || 445),
      smb_share: String(form.get('smb_share') || '').trim(),
      smb_user: String(form.get('smb_user') || '').trim(),
      smb_password: String(form.get('smb_password') || ''),
      smb_domain: String(form.get('smb_domain') || '').trim(),
      webdav_url: String(form.get('webdav_url') || '').trim(),
      webdav_user: String(form.get('webdav_user') || '').trim(),
      webdav_password: String(form.get('webdav_password') || ''),
      enabled: form.get('enabled') === 'on'
    };
    await fetchJSON('api/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    el.mountForm.reset();
    state.mountRootPaths = [];
    renderMountRootPaths();
    renderRootDirectorySelect();
    if (el.mountBrowsePathInput) el.mountBrowsePathInput.value = '/';
    if (el.mountTreeRoot) el.mountTreeRoot.innerHTML = '';
    state.mountTreeExpanded.clear();
    el.mountForm.querySelector('input[name="smb_port"]').value = '445';
    el.mountForm.querySelector('input[name="enabled"]').checked = true;
    setMountTypeUI();
    await loadMounts();
  }

  async function loadUsers() {
    if (!el.userList) return;
    const data = await fetchJSON('api/admin/users');
    if (!data) return;
    el.userList.innerHTML = '';
    (data.items || []).forEach(u => {
      const li = document.createElement('li');
      li.innerHTML = `<span>${escapeHtml(u.username)} ${u.is_admin ? '(admin)' : ''}</span><span>#${u.id}</span>`;
      el.userList.appendChild(li);
    });
  }

  async function submitUserForm(e) {
    e.preventDefault();
    const form = new FormData(el.userForm);
    await fetchJSON('api/admin/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: String(form.get('username') || '').trim(),
        password: String(form.get('password') || ''),
        is_admin: form.get('is_admin') === 'on'
      })
    });
    el.userForm.reset();
    await loadUsers();
  }

  async function triggerScan() {
    await fetchJSON('api/scan', { method: 'POST' });
    await loadScanStatus();
  }

  async function loadScanStatus() {
    if (!el.scanStatusText) return;
    const data = await fetchJSON('api/scan/status');
    if (!data) return;
    if (data.running) {
      const mountTotal = Number(data.mount_total || 0);
      const mountDone = Number(data.mount_done || 0);
      const filesScanned = Number(data.files_scanned || 0);
      const currentMount = String(data.current_mount || '');
      el.scanStatusText.textContent = `扫描中... 挂载 ${mountDone}/${mountTotal}, 已发现 ${filesScanned} 本${currentMount ? ', 当前: ' + currentMount : ''}`;
      lastScanRunning = true;
      lastScannedCount = filesScanned;
      return;
    }
    lastScanRunning = false;
    if (data.last_error) {
      el.scanStatusText.textContent = `上次扫描错误: ${data.last_error}`;
      return;
    }
    if (data.last_at) {
      el.scanStatusText.textContent = `上次扫描: ${new Date(data.last_at).toLocaleString()}`;
      return;
    }
    el.scanStatusText.textContent = '';
  }

  function bindCommonEvents() {
    if (el.logoutBtn) el.logoutBtn.addEventListener('click', logout);
  }

  async function initLibrary() {
    showView('library');
    if (el.libraryPageSize) {
      el.libraryPageSize.value = String(normalizeLibraryPageSize(state.library.pageSize));
    }
    await loadMounts();
    restoreLibraryState();
    await loadComicFolderOptions(el.folderFilter, el.mountFilter ? el.mountFilter.value : '', {});
    updateLibraryPagerUI();
    await loadComics();
    applyPendingLibraryScroll();

    if (el.searchInput) {
      el.searchInput.addEventListener('input', () => {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(async () => {
          state.library.page = 1;
          await loadComics();
          window.scrollTo({ top: 0, behavior: 'auto' });
          saveLibraryState(0);
        }, 240);
      });
    }
    if (el.mountFilter) {
      el.mountFilter.addEventListener('change', async () => {
        if (el.folderFilter) {
          el.folderFilter.dataset.pendingValue = '';
          await loadComicFolderOptions(el.folderFilter, el.mountFilter.value, {});
        }
        state.library.page = 1;
        await loadComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
        saveLibraryState(0);
      });
    }
    if (el.sortRule) {
      el.sortRule.addEventListener('change', async () => {
        state.library.page = 1;
        await loadComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
        saveLibraryState(0);
      });
    }
    if (el.statusFilter) {
      el.statusFilter.addEventListener('change', async () => {
        state.library.page = 1;
        await loadComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
        saveLibraryState(0);
      });
    }
    if (el.folderFilter) {
      el.folderFilter.addEventListener('change', async () => {
        state.library.page = 1;
        await loadComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
        saveLibraryState(0);
      });
    }
    if (el.refreshBtn) {
      el.refreshBtn.addEventListener('click', async () => {
        await loadMounts();
        await loadComicFolderOptions(el.folderFilter, el.mountFilter ? el.mountFilter.value : '', {});
        await loadComics();
      });
    }
    if (el.libraryPageSize) {
      el.libraryPageSize.addEventListener('change', async () => {
        state.library.pageSize = normalizeLibraryPageSize(el.libraryPageSize.value);
        state.library.page = 1;
        await loadComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
        saveLibraryState(0);
      });
    }
    if (el.libraryPrevPageBtn) {
      el.libraryPrevPageBtn.addEventListener('click', async () => {
        await changeLibraryPage(state.library.page - 1, true);
      });
    }
    if (el.libraryNextPageBtn) {
      el.libraryNextPageBtn.addEventListener('click', async () => {
        await changeLibraryPage(state.library.page + 1, true);
      });
    }
    window.addEventListener('scroll', onLibraryScroll, { passive: true });
  }

  async function initHistory() {
    showView('history');
    await loadHistory();
  }

  async function initFavorites() {
    showView('favorites');
    if (el.favoritesPageSize) {
      el.favoritesPageSize.value = String(normalizeLibraryPageSize(state.favorites.pageSize));
    }
    await loadMounts();
    await loadComicFolderOptions(el.favoritesFolderFilter, el.favoritesMountFilter ? el.favoritesMountFilter.value : '', { favoriteOnly: true });
    updateFavoritesPagerUI();
    await loadFavoriteComics();

    if (el.favoritesMountFilter) {
      el.favoritesMountFilter.addEventListener('change', async () => {
        if (el.favoritesFolderFilter) {
          el.favoritesFolderFilter.dataset.pendingValue = '';
          await loadComicFolderOptions(el.favoritesFolderFilter, el.favoritesMountFilter.value, { favoriteOnly: true });
        }
        state.favorites.page = 1;
        await loadFavoriteComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
      });
    }
    if (el.favoritesFolderFilter) {
      el.favoritesFolderFilter.addEventListener('change', async () => {
        state.favorites.page = 1;
        await loadFavoriteComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
      });
    }
    if (el.favoritesPageSize) {
      el.favoritesPageSize.addEventListener('change', async () => {
        state.favorites.pageSize = normalizeLibraryPageSize(el.favoritesPageSize.value);
        state.favorites.page = 1;
        await loadFavoriteComics();
        window.scrollTo({ top: 0, behavior: 'auto' });
      });
    }
    if (el.favoritesPrevPageBtn) {
      el.favoritesPrevPageBtn.addEventListener('click', async () => {
        await changeFavoritesPage(state.favorites.page - 1, true);
      });
    }
    if (el.favoritesNextPageBtn) {
      el.favoritesNextPageBtn.addEventListener('click', async () => {
        await changeFavoritesPage(state.favorites.page + 1, true);
      });
    }
    if (el.favoritesRefreshBtn) {
      el.favoritesRefreshBtn.addEventListener('click', async () => {
        await loadMounts();
        await loadComicFolderOptions(el.favoritesFolderFilter, el.favoritesMountFilter ? el.favoritesMountFilter.value : '', { favoriteOnly: true });
        await loadFavoriteComics();
      });
    }
  }

  async function initReader() {
    showView('reader');
    bindReaderControls();
    await loadReader();
  }

  async function initSettings() {
    showView('settings');
    initSettingsPage();
    initSettingsIndex();

    document.querySelectorAll('.admin-only').forEach((node) => {
      node.hidden = !isAdmin;
    });
    const hashSection = String(location.hash || '').replace(/^#/, '').trim();
    const defaultSection = hashSection || 'reader';
    showSettingsSection(defaultSection);

    if (!isAdmin) return;

    await loadRuntimeSettings();
    await loadRootDirectories();
    await loadMounts();
    await loadUsers();
    await loadScanStatus();
    await loadArchiveTasks();
    if (state.archiveTask.selectedID) {
      await loadArchiveTaskDetail(true);
    }
    if (hasActiveArchiveTask()) {
      ensureArchiveTaskPolling();
    }
    await loadWebPTasks();
    if (state.rootDirectories.length > 0) {
      try {
        await loadWebPSourceTree('');
      } catch (err) {
        renderWebPTaskSummary(null);
      }
    }
    resetNameFormatPreviewState();

    setMountTypeUI();
    renderMountRootPaths();
    if (el.mountType && el.mountType.dataset.bound !== '1') {
      el.mountType.dataset.bound = '1';
      el.mountType.addEventListener('change', setMountTypeUI);
    }
    if (el.rootDirectoryForm && el.rootDirectoryForm.dataset.bound !== '1') {
      el.rootDirectoryForm.dataset.bound = '1';
      el.rootDirectoryForm.addEventListener('submit', submitRootDirectoryForm);
    }
    if (el.mountRootDirectorySelect && el.mountRootDirectorySelect.dataset.bound !== '1') {
      el.mountRootDirectorySelect.dataset.bound = '1';
      el.mountRootDirectorySelect.addEventListener('change', async () => {
        if (el.mountBrowsePathInput) el.mountBrowsePathInput.value = '/';
        if (el.mountTreeRoot) el.mountTreeRoot.innerHTML = '';
        state.mountTreeExpanded.clear();
        if (el.mountBrowseRootBtn) el.mountBrowseRootBtn.click();
      });
    }
    if (el.mountBrowseRootBtn && el.mountBrowseRootBtn.dataset.bound !== '1') {
      el.mountBrowseRootBtn.dataset.bound = '1';
      el.mountBrowseRootBtn.addEventListener('click', async () => {
        if (!state.rootDirectories || state.rootDirectories.length === 0) {
          alert('请先在“根目录库”中新增根目录');
          return;
        }
        const browsePath = el.mountBrowsePathInput ? String(el.mountBrowsePathInput.value || '/').trim() : '/';
        try {
          await loadMountTree(browsePath || '/');
        } catch (err) {
          alert('加载目录失败: ' + (err?.message || String(err)));
        }
      });
    }
    if (el.mountForm && el.mountForm.dataset.bound !== '1') {
      el.mountForm.dataset.bound = '1';
      el.mountForm.addEventListener('submit', submitMountForm);
    }
    if (el.userForm && el.userForm.dataset.bound !== '1') {
      el.userForm.dataset.bound = '1';
      el.userForm.addEventListener('submit', submitUserForm);
    }
    if (el.scanBtn && el.scanBtn.dataset.bound !== '1') {
      el.scanBtn.dataset.bound = '1';
      el.scanBtn.addEventListener('click', triggerScan);
    }
    if (el.archiveCollectBtn && el.archiveCollectBtn.dataset.bound !== '1') {
      el.archiveCollectBtn.dataset.bound = '1';
      el.archiveCollectBtn.addEventListener('click', () => {
        void runArchiveWorkflow('collect');
      });
    }
    if (el.archiveExtractBtn && el.archiveExtractBtn.dataset.bound !== '1') {
      el.archiveExtractBtn.dataset.bound = '1';
      el.archiveExtractBtn.addEventListener('click', () => {
        void runArchiveWorkflow('extract');
      });
    }
    if (el.archiveOrganizeBtn && el.archiveOrganizeBtn.dataset.bound !== '1') {
      el.archiveOrganizeBtn.dataset.bound = '1';
      el.archiveOrganizeBtn.addEventListener('click', () => {
        void runArchiveWorkflow('organize');
      });
    }
    if (el.archiveRepackBtn && el.archiveRepackBtn.dataset.bound !== '1') {
      el.archiveRepackBtn.dataset.bound = '1';
      el.archiveRepackBtn.addEventListener('click', () => {
        void runArchiveWorkflow('repack');
      });
    }
    if (el.archiveWorkflowMount && el.archiveWorkflowMount.dataset.bound !== '1') {
      el.archiveWorkflowMount.dataset.bound = '1';
      el.archiveWorkflowMount.addEventListener('change', () => {
        resetNameFormatPreviewState();
      });
    }
    if (el.nameFormatPreviewBtn && el.nameFormatPreviewBtn.dataset.bound !== '1') {
      el.nameFormatPreviewBtn.dataset.bound = '1';
      el.nameFormatPreviewBtn.addEventListener('click', () => {
        void previewNameFormat();
      });
    }
    if (el.nameFormatApplyBtn && el.nameFormatApplyBtn.dataset.bound !== '1') {
      el.nameFormatApplyBtn.dataset.bound = '1';
      el.nameFormatApplyBtn.addEventListener('click', () => {
        void applyNameFormat();
      });
    }
    if (el.archiveWorkflowOutput && !el.archiveWorkflowOutput.textContent) {
      el.archiveWorkflowOutput.textContent = '等待执行任务...';
    }
    if (el.archiveTaskRefreshBtn && el.archiveTaskRefreshBtn.dataset.bound !== '1') {
      el.archiveTaskRefreshBtn.dataset.bound = '1';
      el.archiveTaskRefreshBtn.addEventListener('click', async () => {
        await loadArchiveTasks();
        if (state.archiveTask.selectedID) {
          await loadArchiveTaskDetail(false);
        }
      });
    }
    if (el.webpConvertSourceRoot && el.webpConvertSourceRoot.dataset.bound !== '1') {
      el.webpConvertSourceRoot.dataset.bound = '1';
      el.webpConvertSourceRoot.addEventListener('change', async () => {
        state.webpConvert.treeExpanded.clear();
        if (el.webpConvertSourcePath) el.webpConvertSourcePath.value = '';
        if (el.webpConvertTreeRoot) el.webpConvertTreeRoot.innerHTML = '';
        if (el.webpConvertBrowsePath) el.webpConvertBrowsePath.value = '';
        try {
          await loadWebPSourceTree('');
        } catch (err) {
          alert('加载目录失败: ' + (err?.message || String(err)));
        }
      });
    }
    if (el.webpConvertBrowseRootBtn && el.webpConvertBrowseRootBtn.dataset.bound !== '1') {
      el.webpConvertBrowseRootBtn.dataset.bound = '1';
      el.webpConvertBrowseRootBtn.addEventListener('click', async () => {
        state.webpConvert.treeExpanded.clear();
        try {
          await loadWebPSourceTree('');
        } catch (err) {
          alert('加载目录失败: ' + (err?.message || String(err)));
        }
      });
    }
    if (el.webpConvertSaveDefaultsBtn && el.webpConvertSaveDefaultsBtn.dataset.bound !== '1') {
      el.webpConvertSaveDefaultsBtn.dataset.bound = '1';
      el.webpConvertSaveDefaultsBtn.addEventListener('click', () => {
        void saveWebPDefaults();
      });
    }
    if (el.webpConvertRunBtn && el.webpConvertRunBtn.dataset.bound !== '1') {
      el.webpConvertRunBtn.dataset.bound = '1';
      el.webpConvertRunBtn.addEventListener('click', () => {
        void runWebPConvertTask();
      });
    }
    if (el.webpConvertTaskRefreshBtn && el.webpConvertTaskRefreshBtn.dataset.bound !== '1') {
      el.webpConvertTaskRefreshBtn.dataset.bound = '1';
      el.webpConvertTaskRefreshBtn.addEventListener('click', async () => {
        await loadWebPTasks();
      });
    }
    if (el.webpConvertCancelBtn && el.webpConvertCancelBtn.dataset.bound !== '1') {
      el.webpConvertCancelBtn.dataset.bound = '1';
      el.webpConvertCancelBtn.addEventListener('click', () => {
        void cancelWebPTask();
      });
    }
    renderWebPTaskSummary(null);

    if (el.mountBrowseRootBtn) {
      el.mountBrowseRootBtn.click();
    }

    if (scanTimer) clearInterval(scanTimer);
    scanTimer = setInterval(loadScanStatus, 5000);
  }

  async function init() {
    if (el.userBadge) el.userBadge.textContent = `${username}${isAdmin ? ' (admin)' : ''}`;
    try {
      await loadSettingsFromServer();
    } catch (_) {
      state.reader.settings = cloneDefaultSettings();
    }
    bindCommonEvents();

    try {
      if (view === 'reader') {
        await initReader();
      } else if (view === 'favorites') {
        await initFavorites();
      } else if (view === 'history') {
        await initHistory();
      } else if (view === 'settings') {
        await initSettings();
      } else {
        await initLibrary();
      }
    } catch (err) {
      alert(err.message || String(err));
    }
  }

  init();

  let appCleanedUp = false;

  function cleanupAppResources() {
    if (appCleanedUp) return;
    appCleanedUp = true;
    if (scanTimer) clearInterval(scanTimer);
    if (state.archiveTask.pollTimer) clearInterval(state.archiveTask.pollTimer);
    if (libraryScrollTimer) clearTimeout(libraryScrollTimer);
    if (resizeTimer) clearTimeout(resizeTimer);
    if (settingsSaveTimer) clearTimeout(settingsSaveTimer);
    if (state.reader.saveTimer) clearTimeout(state.reader.saveTimer);
    if (state.scrollObserver) state.scrollObserver.disconnect();
    if (state.pdfCoverObserver) state.pdfCoverObserver.disconnect();
    state.pdfCoverCache.forEach((url) => {
      if (url) URL.revokeObjectURL(url);
    });
    state.pdfCoverCache.clear();
    state.pdfCoverTaskMap.clear();
    state.pdfCoverQueue.length = 0;
    clearReaderImageCache();
    if (state.windowScrollBound) window.removeEventListener('scroll', onWindowScroll);
    window.removeEventListener('scroll', onLibraryScroll);
    void destroyReaderPDF();
  }

  window.addEventListener('pagehide', cleanupAppResources, { capture: true });
})();
