(function (root) {
  'use strict';
  const core = {
    pageScale(iw, ih, width, height, fit) {
      if (fit === 'original') return 1;
      if (fit === 'contain' || fit === 'height') return Math.min(width / iw, height / ih);
      return width / iw;
    },
    clampListPage(page, totalPages) {
      return Math.max(1, Math.min(Math.max(1, Math.floor(Number(page) || 1)), Math.max(1, totalPages)));
    },
    turnPage(page, direction, total, double, cover) {
      let next = page + direction;
      if (double) {
        if (direction > 0) next = page + (cover && page === 0 ? 1 : 2);
        else next = Math.max(cover ? 1 : 0, page - 2);
        if (direction < 0 && cover && page <= 1) next = 0;
      }
      return Math.max(0, Math.min(Math.max(0, total - 1), next));
    },
    newerState(a, b) {
      if (!a) return b;
      if (!b) return a;
      return Date.parse(b.updated_at || '') > Date.parse(a.updated_at || '') ? b : a;
    }
  };
  if (typeof module !== 'undefined' && module.exports) module.exports = core;
  else root.ReaderCore = core;
})(typeof globalThis !== 'undefined' ? globalThis : this);
