/* LLM Proxy console: pure formatting helpers.
 *
 * These helpers are stateless and DOM-free, so they live in their own file
 * instead of the view code in app.js. The console has no build step, so the
 * module publishes itself on globalThis and app.js destructures it. Load this
 * file before app.js.
 */
(function () {
  "use strict";

  function plural(count, singular, many) {
    return Number(count) === 1 ? singular : many || singular + "s";
  }

  function formatNumber(value) {
    return Number(value || 0).toLocaleString();
  }

  function countLabel(visible, total, noun) {
    const word = plural(total, noun);
    if (visible === total) return formatNumber(total) + " " + word;
    return formatNumber(visible) + " of " + formatNumber(total) + " " + word;
  }

  function matchesQuery(values, term) {
    if (!term) return true;
    return (
      values
        .filter((value) => value !== undefined && value !== null)
        .join(" ")
        .toLowerCase()
        .indexOf(term) >= 0
    );
  }

  function titleCase(value) {
    return String(value || "")
      .split(/[\s_-]+/)
      .filter(Boolean)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
      .join(" ");
  }

  // scaled renders a number in the first matching unit and trims trailing
  // zeros, so callers only have to describe their unit table and precision.
  function scaled(value, units, digitsFor) {
    const number = Number(value || 0);
    if (!Number.isFinite(number) || number <= 0) return "0";
    for (let index = 0; index < units.length; index += 1) {
      const size = units[index][0];
      if (number >= size) {
        const scaledValue = number / size;
        const digits = digitsFor(scaledValue);
        const text = digits > 0 ? scaledValue.toFixed(digits).replace(/\.?0+$/, "") : scaledValue.toFixed(0);
        return text + units[index][1];
      }
    }
    return String(Math.round(number));
  }

  function formatSize(value) {
    return scaled(
      value,
      [
        [1024 * 1024 * 1024, "G"],
        [1024 * 1024, "M"],
        [1024, "K"],
      ],
      (amount) => (amount >= 100 ? 0 : amount >= 10 ? 1 : 2)
    );
  }

  function compact(value) {
    return scaled(
      value,
      [
        [1e9, "B"],
        [1e6, "M"],
        [1e3, "K"],
      ],
      (amount) => (amount >= 100 ? 0 : 1)
    );
  }

  function formatDuration(ms) {
    const value = Number(ms || 0);
    if (value < 1000) return Math.max(0, Math.round(value)) + " ms";
    if (value < 60000) return (value / 1000).toFixed(2) + " s";
    return (value / 60000).toFixed(1) + " min";
  }

  // formatRate renders a decode throughput in output tokens per second.
  function formatRate(value) {
    const number = Number(value || 0);
    if (!Number.isFinite(number) || number <= 0) return "—";
    return (number >= 100 ? Math.round(number) : number.toFixed(1)) + " tok/s";
  }

  // formatLatency renders a duration that may be absent. A missing sample shows
  // as an em dash instead of a misleading 0 ms.
  function formatLatency(ms) {
    const value = Number(ms || 0);
    if (value <= 0) return "—";
    return formatDuration(value);
  }

  // formatTTFT renders time-to-first-token. The server only records it for
  // replies it actually streamed and never samples a failed request.
  function formatTTFT(record) {
    if (Number(record.status || 0) >= 400) return "—";
    return formatLatency(record.ttft_ms);
  }

  // outputRate derives one request's decode throughput from its total duration,
  // its time-to-first-token and its output tokens. It mirrors the server-side
  // aggregation, which also skips failed requests.
  function outputRate(record) {
    if (Number(record.status || 0) >= 400) return 0;
    const duration = Number(record.duration_ms || 0);
    const ttft = Number(record.ttft_ms || 0);
    const output = Number(record.output_tokens || 0);
    if (output <= 0 || ttft <= 0 || duration <= ttft) return 0;
    return output / ((duration - ttft) / 1000);
  }

  function formatDate(iso) {
    if (!iso) return "—";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "—";
    return date.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  function formatClock(iso) {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "";
    return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }

  function timeAgo(iso) {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "—";
    const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
    if (seconds < 10) return "just now";
    if (seconds < 60) return seconds + "s ago";
    if (seconds < 3600) return Math.floor(seconds / 60) + "m ago";
    if (seconds < 86400) return Math.floor(seconds / 3600) + "h ago";
    return Math.floor(seconds / 86400) + "d ago";
  }

  function formatUptime(seconds) {
    const total = Math.max(0, Number(seconds || 0));
    if (total < 60) return Math.round(total) + "s";
    if (total < 3600) return Math.floor(total / 60) + "m " + Math.round(total % 60) + "s";
    if (total < 86400) return Math.floor(total / 3600) + "h " + Math.floor((total % 3600) / 60) + "m";
    return Math.floor(total / 86400) + "d " + Math.floor((total % 86400) / 3600) + "h";
  }

  globalThis.LPFormat = {
    plural,
    formatNumber,
    countLabel,
    matchesQuery,
    titleCase,
    scaled,
    formatSize,
    compact,
    formatDuration,
    formatLatency,
    formatRate,
    formatTTFT,
    outputRate,
    formatDate,
    formatClock,
    timeAgo,
    formatUptime,
  };
})();
