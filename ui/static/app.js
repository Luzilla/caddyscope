document.addEventListener("alpine:init", () => {
  const basePath = document.querySelector('meta[name="base-path"]')?.content || "";

  Alpine.store("dashboard", {
    vhosts: [],
    stats: [],
    warnings: [],
    timestamp: null,
    filter: "",
    sortBy: "host",
    sortAsc: true,
    connected: false,
    selectedHost: null,
    theme: localStorage.getItem("cs-theme") || "auto",

    _prev: {},
    _prevTime: null,
    _history: {},
    _maxPoints: 60,

    get filteredStats() {
      const vhostMap = {};
      for (const vh of this.vhosts) {
        vhostMap[vh.host] = vh;
      }

      let merged = this.stats.map((s) => {
        const vh = vhostMap[s.host] || {};
        const hasPrev = s.host in this._prev;
        const prevTotal = this._prev[s.host] || 0;
        const now = Date.now();
        const elapsed = this._prevTime ? (now - this._prevTime) / 1000 : 0;
        const rate = hasPrev && elapsed > 0 ? (s.total_requests - prevTotal) / elapsed : 0;

        const codes = s.status_codes || {};
        const errors = Object.entries(codes)
          .filter(([c]) => c.startsWith("4") || c.startsWith("5"))
          .reduce((sum, [, v]) => sum + v, 0);
        const errorRate = s.total_requests > 0 ? (errors / s.total_requests) * 100 : 0;

        return {
          host: s.host,
          server: vh.server || "",
          listen: vh.listen || [],
          upstreams: vh.upstreams || [],
          total_requests: s.total_requests,
          status_codes: codes,
          duration_p50: s.duration_p50 || 0,
          duration_p95: s.duration_p95 || 0,
          duration_p99: s.duration_p99 || 0,
          rate: Math.max(0, rate),
          errorRate,
        };
      });

      // Filter
      if (this.filter) {
        const q = this.filter.toLowerCase();
        merged = merged.filter((s) => s.host.toLowerCase().includes(q));
      }

      // Sort
      const key = this.sortBy;
      const asc = this.sortAsc;
      merged.sort((a, b) => {
        let va = a[key];
        let vb = b[key];
        if (typeof va === "string") {
          va = va.toLowerCase();
          vb = vb.toLowerCase();
        }
        if (va < vb) return asc ? -1 : 1;
        if (va > vb) return asc ? 1 : -1;
        return 0;
      });

      return merged;
    },

    handleEvent(data) {
      const now = Date.now();
      // Save prev counters for rate computation
      const prev = {};
      for (const s of this.stats) {
        prev[s.host] = s.total_requests;
      }
      this._prev = prev;
      this._prevTime = now;

      this.vhosts = data.vhosts;
      this.stats = data.stats;
      this.warnings = data.warnings || [];
      this.timestamp = data.timestamp;

      // Accumulate history (latency + rate/error computed from filteredStats)
      const t = new Date(data.timestamp);
      const computed = {};
      for (const s of this.filteredStats) {
        computed[s.host] = s;
      }
      for (const s of data.stats) {
        if (!this._history[s.host]) this._history[s.host] = [];
        const arr = this._history[s.host];
        const c = computed[s.host];
        arr.push({
          time: t,
          p50: s.duration_p50 * 1000,
          p95: s.duration_p95 * 1000,
          p99: s.duration_p99 * 1000,
          rate: c ? c.rate : 0,
          errorRate: c ? c.errorRate : 0,
        });
        if (arr.length > this._maxPoints) arr.splice(0, arr.length - this._maxPoints);
      }
    },

    get themeLabel() {
      return this.theme === "auto" ? "Auto" : this.theme === "light" ? "Light" : "Dark";
    },

    cycleTheme() {
      const modes = ["auto", "light", "dark"];
      this.theme = modes[(modes.indexOf(this.theme) + 1) % 3];
      localStorage.setItem("cs-theme", this.theme);
      this._applyTheme();
    },

    _applyTheme() {
      const isDark = this.theme === "dark" ||
        (this.theme === "auto" && window.matchMedia("(prefers-color-scheme: dark)").matches);
      document.documentElement.classList.toggle("dark", isDark);
    },

    get healthyCounts() {
      let healthy = 0;
      let unhealthy = 0;
      for (const s of this.filteredStats) {
        if (s.upstreams.length === 0 || s.upstreams.every(u => u.healthy)) {
          healthy++;
        } else {
          unhealthy++;
        }
      }
      return { healthy, unhealthy };
    },

    get selectedStat() {
      if (!this.selectedHost) return null;
      return this.filteredStats.find(s => s.host === this.selectedHost) || null;
    },

    openDrawer(host) {
      this.selectedHost = host;
    },

    closeDrawer() {
      this.selectedHost = null;
    },

    renderSparkline(host, el) {
      // Read timestamp to create reactive dependency so Alpine re-runs on each tick
      void this.timestamp;
      const points = this._history[host];
      if (!points || points.length < 2) {
        el.replaceChildren();
        return;
      }
      const values = points.map(p => p.p95);
      const min = Math.min(...values);
      const max = Math.max(...values);
      const range = max - min || 1;
      const w = 80;
      const h = 24;
      const step = w / (values.length - 1);
      const coords = values.map((v, i) =>
        `${(i * step).toFixed(1)},${(h - ((v - min) / range) * h).toFixed(1)}`
      ).join(" ");
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("viewBox", `0 0 ${w} ${h}`);
      svg.setAttribute("preserveAspectRatio", "none");
      const polyline = document.createElementNS("http://www.w3.org/2000/svg", "polyline");
      polyline.setAttribute("points", coords);
      polyline.setAttribute("fill", "none");
      polyline.setAttribute("stroke", "#3b82f6");
      polyline.setAttribute("stroke-width", "1.5");
      polyline.setAttribute("stroke-linejoin", "round");
      svg.appendChild(polyline);
      el.replaceChildren(svg);
    },

    renderChart(host, el) {
      const points = this._history[host];
      if (!points || points.length < 2 || typeof Plot === "undefined") return;

      const data = points.flatMap(p => [
        { time: p.time, latency: p.p50, percentile: "p50" },
        { time: p.time, latency: p.p95, percentile: "p95" },
        { time: p.time, latency: p.p99, percentile: "p99" },
      ]);

      // Separate series for areas — draw back-to-front (p99, p95, p50)
      const p99 = points.map(p => ({ time: p.time, latency: p.p99 }));
      const p95 = points.map(p => ({ time: p.time, latency: p.p95 }));
      const p50 = points.map(p => ({ time: p.time, latency: p.p50 }));

      const chart = Plot.plot({
        width: el.clientWidth || 500,
        height: 180,
        style: { background: "transparent", fontSize: "11px" },
        color: { domain: ["p50", "p95", "p99"], range: ["#22c55e", "#f59e0b", "#ef4444"] },
        x: { type: "time", label: null },
        y: { label: "ms", grid: true },
        marks: [
          Plot.areaY(p99, { x: "time", y: "latency", fill: "#ef4444", fillOpacity: 0.1, curve: "monotone-x" }),
          Plot.areaY(p95, { x: "time", y: "latency", fill: "#f59e0b", fillOpacity: 0.1, curve: "monotone-x" }),
          Plot.areaY(p50, { x: "time", y: "latency", fill: "#22c55e", fillOpacity: 0.1, curve: "monotone-x" }),
          Plot.lineY(data, { x: "time", y: "latency", stroke: "percentile", strokeWidth: 1.5, curve: "monotone-x" }),
          Plot.tip(data, Plot.pointerX({ x: "time", y: "latency", stroke: "percentile" })),
        ],
      });

      el.replaceChildren(chart);
    },

    renderRateChart(host, el) {
      void this.timestamp;
      const points = this._history[host];
      if (!points || points.length < 2 || typeof Plot === "undefined") return;

      const data = points.map(p => ({ time: p.time, rate: p.rate }));

      const chart = Plot.plot({
        width: el.clientWidth || 500,
        height: 120,
        style: { background: "transparent", fontSize: "11px" },
        x: { type: "time", label: null },
        y: { label: "req/s", grid: true },
        marks: [
          Plot.areaY(data, { x: "time", y: "rate", fill: "#3b82f6", fillOpacity: 0.15, curve: "monotone-x" }),
          Plot.lineY(data, { x: "time", y: "rate", stroke: "#3b82f6", strokeWidth: 1.5, curve: "monotone-x" }),
          Plot.tip(data, Plot.pointerX({ x: "time", y: "rate" })),
        ],
      });

      el.replaceChildren(chart);
    },

    renderErrorChart(host, el) {
      void this.timestamp;
      const points = this._history[host];
      if (!points || points.length < 2 || typeof Plot === "undefined") return;

      const data = points.map(p => ({ time: p.time, errorRate: p.errorRate }));

      const chart = Plot.plot({
        width: el.clientWidth || 500,
        height: 120,
        style: { background: "transparent", fontSize: "11px" },
        x: { type: "time", label: null },
        y: { label: "% err", grid: true },
        marks: [
          Plot.areaY(data, { x: "time", y: "errorRate", fill: "#ef4444", fillOpacity: 0.15, curve: "monotone-x" }),
          Plot.lineY(data, { x: "time", y: "errorRate", stroke: "#ef4444", strokeWidth: 1.5, curve: "monotone-x" }),
          Plot.tip(data, Plot.pointerX({ x: "time", y: "errorRate" })),
        ],
      });

      el.replaceChildren(chart);
    },

    toggleSort(field) {
      if (this.sortBy === field) {
        this.sortAsc = !this.sortAsc;
      } else {
        this.sortBy = field;
        this.sortAsc = field === "host";
      }
    },
  });

  // Apply saved theme and listen for system changes
  const store = Alpine.store("dashboard");
  store._applyTheme();
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    store._applyTheme();
  });

  // SSE connection with exponential backoff
  let retryDelay = 1000;
  const maxDelay = 30000;

  function connect() {
    const store = Alpine.store("dashboard");
    const es = new EventSource(basePath + "/api/stream");

    es.addEventListener("stats", (e) => {
      try {
        const data = JSON.parse(e.data);
        store.handleEvent(data);
        store.connected = true;
        retryDelay = 1000;
      } catch (err) {
        console.error("Failed to parse SSE data:", err);
      }
    });

    es.addEventListener("open", () => {
      store.connected = true;
      retryDelay = 1000;
    });

    es.addEventListener("error", () => {
      store.connected = false;
      es.close();
      setTimeout(() => {
        retryDelay = Math.min(retryDelay * 2, maxDelay);
        connect();
      }, retryDelay);
    });
  }

  connect();
});
