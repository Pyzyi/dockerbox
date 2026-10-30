// Appearance settings -> CSS custom properties. Loaded synchronously in <head>
// (classic script, no module) so the cached look is applied before the first
// paint. Every value is validated here again: settings end up in CSS.
(() => {
  "use strict";
  const HEX = /^#[0-9a-fA-F]{6}$/;
  const KEY = "dockerbox.settings";

  const ACCENTS = [
    ["blue", "#0a84ff"], ["purple", "#bf5af2"], ["pink", "#ff375f"], ["red", "#ff453a"],
    ["orange", "#ff9f0a"], ["yellow", "#ffd60a"], ["green", "#30d158"], ["teal", "#40c8e0"],
    ["graphite", "#8e8e93"],
  ];

  // Four blob colours per wallpaper, tuned separately for dark and light mode.
  const WALLPAPERS = {
    aurora: { dark: ["#5e3bd6", "#0a6cff", "#ff2d75", "#11b88a"], light: ["#b9a6ff", "#8cc4ff", "#ffb3cf", "#9be8c9"] },
    ocean: { dark: ["#1748c9", "#00a3c4", "#3a2bd0", "#0fd1b5"], light: ["#9cc6ff", "#9fe7f5", "#b9b4ff", "#a6f0e2"] },
    sunset: { dark: ["#ff5e3a", "#c2185b", "#ff9f0a", "#6a1b9a"], light: ["#ffc2a8", "#ffb0cd", "#ffe0a3", "#d7b8f0"] },
    forest: { dark: ["#1b7f4b", "#0e5e6f", "#7cb342", "#2e7d32"], light: ["#b4e3c4", "#a9dbe3", "#d6ecb0", "#bfe3c0"] },
    graphite: { dark: ["#48484a", "#2c2c2e", "#636366", "#3a3a3c"], light: ["#d1d1d6", "#e5e5ea", "#c7c7cc", "#f2f2f7"] },
  };

  const DEFAULTS = Object.freeze({
    theme: "auto", accent: "#0a84ff", wallpaper: "aurora",
    wallColors: ["#5e3bd6", "#0a6cff", "#ff2d75", "#11b88a"],
    glass: 50, motion: true, fontSize: 13, language: "auto",
  });

  const oneOf = (v, list, def) => (list.includes(v) ? v : def);
  const intIn = (v, lo, hi, def) => (Number.isInteger(v) && v >= lo && v <= hi ? v : def);
  const hex = (v, def) => (typeof v === "string" && HEX.test(v) ? v.toLowerCase() : def);

  function sanitize(s) {
    s = s && typeof s === "object" ? s : {};
    const wc = Array.isArray(s.wallColors) ? s.wallColors : [];
    return {
      theme: oneOf(s.theme, ["auto", "dark", "light"], DEFAULTS.theme),
      accent: hex(s.accent, DEFAULTS.accent),
      wallpaper: oneOf(s.wallpaper, [...Object.keys(WALLPAPERS), "custom"], DEFAULTS.wallpaper),
      wallColors: DEFAULTS.wallColors.map((d, i) => hex(wc[i], d)),
      glass: intIn(s.glass, 0, 100, DEFAULTS.glass),
      motion: typeof s.motion === "boolean" ? s.motion : DEFAULTS.motion,
      fontSize: intIn(s.fontSize, 10, 22, DEFAULTS.fontSize),
      language: oneOf(s.language, ["auto", "en", "de"], DEFAULTS.language),
    };
  }

  const lightQuery = matchMedia("(prefers-color-scheme: light)");
  const resolveTheme = (pref) => (pref === "auto" ? (lightQuery.matches ? "light" : "dark") : pref);

  let current = null;

  function apply(input) {
    const s = sanitize(input);
    const root = document.documentElement;
    const theme = resolveTheme(s.theme);
    root.dataset.theme = theme;
    root.dataset.motion = s.motion ? "on" : "off";
    root.style.setProperty("--accent", s.accent);
    const walls = s.wallpaper === "custom" ? s.wallColors : WALLPAPERS[s.wallpaper][theme];
    walls.forEach((c, i) => root.style.setProperty(`--wall-${i + 1}`, c));
    // 0 = solid frosted panels, 100 = nearly clear glass.
    root.style.setProperty("--glass-opacity", `${Math.round(90 - s.glass * 0.8)}%`);
    root.style.setProperty("--blur", `blur(${Math.round(12 + s.glass * 0.36)}px) saturate(150%)`);
    const meta = document.querySelector('meta[name="theme-color"]:not([media])');
    if (meta) meta.content = theme === "light" ? "#eef1f8" : "#0b0d14";
    current = s;
    return s;
  }

  function cache(s) {
    try { localStorage.setItem(KEY, JSON.stringify(sanitize(s))); } catch { /* storage unavailable */ }
  }

  function cached() {
    try { return JSON.parse(localStorage.getItem(KEY)); } catch { return null; }
  }

  apply(cached());
  lightQuery.addEventListener("change", () => apply(current));

  window.DockerboxTheme = Object.freeze({
    ACCENTS, WALLPAPERS, DEFAULTS, sanitize, apply, cache,
    get current() { return current; },
  });
})();
