// Evaluated before every user script (see jsrun.go). It captures the host bridge functions,
// removes engine globals that are not part of the sandbox API, installs console/print/fs, and
// returns the helpers the Go side calls after the script finished.
//
// Every string crossing the bridge is JSON-encoded: the bridge passes C strings, so a raw NUL
// would truncate them.
(() => {
  "use strict";

  const g = globalThis;
  const hostLog = g.__wc_log;
  const hostFs = typeof g.__wc_fs === "function" ? g.__wc_fs : null;

  for (const name of [
    "__wc_log", "__wc_fs", "std", "os", "bjson", "gc", "scriptArgs", "print", "console",
    "QJS_PROXY_VALUE",
  ]) {
    delete g[name];
  }

  // Nesting limits for the result and for console arguments. Each level costs JS frames from a
  // finite stack (see jsrun.go), and console calls can happen deep inside user code.
  const RESULT_DEPTH = 64;
  const CONSOLE_DEPTH = 16;

  function functionName(fn) {
    return "[Function" + (fn.name ? ": " + fn.name : " (anonymous)") + "]";
  }

  // toPlain converts any value into JSON-safe data: bigint → "123n", NaN/Infinity → string,
  // functions/symbols → descriptive strings, Map → object, Set → array, Error → {name, message},
  // typed arrays → arrays, cycles → "[Circular]", nesting deeper than maxDepth → "[Object]".
  // It uses plain loops (no callbacks) because the engine's call depth is small.
  function toPlain(v, depth, maxDepth, ancestors) {
    switch (typeof v) {
      case "undefined":
        return undefined;
      case "string":
      case "boolean":
        return v;
      case "number":
        return Number.isFinite(v) ? v : String(v);
      case "bigint":
        return v.toString() + "n";
      case "symbol":
        return v.toString();
      case "function":
        return functionName(v);
    }
    if (v === null) return null;
    if (ancestors.includes(v)) return "[Circular]";
    if (depth >= maxDepth) return Array.isArray(v) ? "[Array]" : "[Object]";
    if (typeof v.toJSON === "function" && !(v instanceof Error)) {
      const j = v.toJSON();
      if (typeof j !== "object" || j === null) return toPlain(j, depth, maxDepth, ancestors);
      v = j;
    }

    ancestors.push(v);
    try {
      if (Array.isArray(v)) {
        const out = [];
        for (let i = 0; i < v.length; i++) {
          const x = toPlain(v[i], depth + 1, maxDepth, ancestors);
          out.push(x === undefined ? null : x);
        }
        return out;
      }
      if (v instanceof Error) {
        return { name: String(v.name), message: String(v.message) };
      }
      if (v instanceof Map) {
        const out = {};
        for (const [k, x] of v) {
          const y = toPlain(x, depth + 1, maxDepth, ancestors);
          if (y !== undefined) out[typeof k === "string" ? k : inspect(k)] = y;
        }
        return out;
      }
      if (v instanceof Set) {
        const out = [];
        for (const x of v) {
          const y = toPlain(x, depth + 1, maxDepth, ancestors);
          out.push(y === undefined ? null : y);
        }
        return out;
      }
      if (ArrayBuffer.isView(v)) {
        if (v instanceof DataView) return "[DataView " + v.byteLength + " bytes]";
        const out = [];
        for (let i = 0; i < v.length; i++) out.push(toPlain(v[i], depth + 1, maxDepth, ancestors));
        return out;
      }
      if (v instanceof ArrayBuffer) return "[ArrayBuffer " + v.byteLength + " bytes]";
      if (v instanceof Promise) return "[Promise]";
      if (v instanceof WeakMap) return "[WeakMap]";
      if (v instanceof WeakSet) return "[WeakSet]";

      const out = {};
      const keys = Object.keys(v);
      for (let i = 0; i < keys.length; i++) {
        const y = toPlain(v[keys[i]], depth + 1, maxDepth, ancestors);
        if (y !== undefined) out[keys[i]] = y;
      }
      return out;
    } finally {
      ancestors.pop();
    }
  }

  // serialize returns the JSON text of v, or undefined when v is undefined.
  function serialize(v, maxDepth = RESULT_DEPTH) {
    const p = toPlain(v, 0, maxDepth, []);
    return p === undefined ? undefined : JSON.stringify(p);
  }

  function errorText(e) {
    let s;
    try {
      s = String(e);
    } catch {
      s = "Error";
    }
    if (typeof e.stack === "string" && e.stack !== "") s += "\n" + e.stack.replace(/\n+$/, "");
    return s;
  }

  // inspect formats a non-string console argument.
  function inspect(v) {
    switch (typeof v) {
      case "undefined":
        return "undefined";
      case "bigint":
        return v.toString() + "n";
      case "symbol":
        return v.toString();
      case "function":
        return functionName(v);
      case "object":
        if (v === null) return "null";
        if (v instanceof Error) return errorText(v);
        try {
          return serialize(v, CONSOLE_DEPTH);
        } catch {
          try {
            return String(v);
          } catch {
            return "[object]";
          }
        }
      default:
        return String(v);
    }
  }

  let outputFull = false;

  function emit(level, args) {
    if (outputFull) return;
    let text = "";
    for (let i = 0; i < args.length; i++) {
      if (i > 0) text += " ";
      text += typeof args[i] === "string" ? args[i] : inspect(args[i]);
    }
    if (!hostLog(level, JSON.stringify(text))) outputFull = true;
  }

  const console = {
    log: (...args) => emit("log", args),
    info: (...args) => emit("info", args),
    debug: (...args) => emit("debug", args),
    warn: (...args) => emit("warn", args),
    error: (...args) => emit("error", args),
    dir: (v) => emit("log", [v]),
    assert: (cond, ...args) => {
      if (!cond) emit("error", ["Assertion failed" + (args.length ? ":" : ""), ...args]);
    },
  };
  console.trace = console.debug;
  console.table = console.log;

  const define = (name, value) =>
    Object.defineProperty(g, name, { value, writable: true, configurable: true, enumerable: false });
  define("console", console);
  define("print", console.log);

  if (hostFs !== null) {
    const call = (op, args) => {
      const r = JSON.parse(hostFs(op, JSON.stringify(args)));
      if (r.ok) return r.value;
      const e = new Error(r.message);
      e.code = r.code;
      throw e;
    };
    const str = (fn, name, v) => {
      if (typeof v !== "string") throw new TypeError("fs." + fn + ": " + name + " must be a string");
      return v;
    };
    const fs = {
      read: (path) => call("read", { path: str("read", "path", path) }),
      write: (path, content) => {
        str("write", "path", path);
        if (typeof content !== "string") {
          throw new TypeError(
            "fs.write: content must be a string (use fs.writeJSON or JSON.stringify for other values)",
          );
        }
        call("write", { path, content });
      },
      list: (dir = "/", opts = {}) =>
        call("list", { path: str("list", "dir", dir), recursive: !!(opts && opts.recursive) }),
      glob: (pattern) => call("glob", { pattern: str("glob", "pattern", pattern) }),
      stat: (path) => call("stat", { path: str("stat", "path", path) }),
      exists: (path) => call("stat", { path: str("exists", "path", path) }) !== null,
      delete: (path) => {
        call("delete", { path: str("delete", "path", path) });
      },
      readJSON: (path) => {
        const text = fs.read(path);
        try {
          return JSON.parse(text);
        } catch (e) {
          throw new SyntaxError("fs.readJSON " + path + ": " + e.message);
        }
      },
      writeJSON: (path, value, indent = 2) => {
        const text = JSON.stringify(value, null, indent);
        if (text === undefined) throw new TypeError("fs.writeJSON: value is not serializable");
        fs.write(path, text + "\n");
      },
    };
    define("fs", Object.freeze(fs));
  }

  return { serialize };
})()
