import { build } from "esbuild";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ENTRY_TS = resolve(__dirname, "src/app.ts");
const STYLES_CSS = resolve(__dirname, "src/styles.css");
const OUT_HTML = resolve(__dirname, "../assets/app.html");
const MAX_HTML_BYTES = 500 * 1024;

function escapeInlineScript(js) {
  return js
    .replace(/<\/script/gi, "<\\/script")
    .replace(/<!--/g, "<\\!--");
}

function escapeInlineStyle(css) {
  return css.replace(/<\/style/gi, "<\\/style");
}

// Zod v4 re-exports ~50 locale packs (~250 KB) from locales/index.js.
// Keep only English locale so the single-file MCP App stays lean (< 500 KB).
const trimZodLocalesPlugin = {
  name: "trim-zod-locales",
  setup(buildApi) {
    buildApi.onLoad({ filter: /zod[\\/]v4[\\/]locales[\\/]index\.js$/ }, () => ({
      contents: 'export { default as en } from "./en.js";',
      loader: "js",
    }));
  },
};

function verifyBundle(html, css, js) {
  const bytes = Buffer.byteLength(html, "utf8");
  if (bytes >= MAX_HTML_BYTES) {
    throw new Error(
      `Bundle size ${(bytes / 1024).toFixed(1)} KB exceeds 500 KB limit`
    );
  }

  const htmlWithoutScript = html.replace(/<script>[\s\S]*<\/script>/i, "");

  const htmlChecks = [
    { name: "external <script src>", re: /<script\b[^>]*\bsrc\s*=/i },
    { name: "external <link href>", re: /<link\b[^>]*\bhref\s*=/i },
  ];

  for (const check of htmlChecks) {
    if (check.re.test(html)) {
      throw new Error(`CSP check failed: found ${check.name}`);
    }
  }

  const cssChecks = [
    { name: "external CSS url()", re: /url\(\s*['"]?(?:https?:)?\/\//i },
    { name: "CSS @import", re: /@import\b/i },
  ];

  for (const check of cssChecks) {
    if (check.re.test(css) || check.re.test(htmlWithoutScript)) {
      throw new Error(`CSP check failed: found ${check.name}`);
    }
  }

  const forbiddenStorage = [
    { name: "localStorage", re: /\blocalStorage\b/ },
    { name: "sessionStorage", re: /\bsessionStorage\b/ },
    { name: "document.cookie", re: /\bdocument\.cookie\b/ },
  ];

  for (const check of forbiddenStorage) {
    if (check.re.test(js)) {
      throw new Error(`Sandbox check failed: found ${check.name}`);
    }
  }

  return bytes;
}

const [jsResult, cssRaw] = await Promise.all([
  build({
    entryPoints: [ENTRY_TS],
    bundle: true,
    format: "iife",
    minify: true,
    treeShaking: true,
    target: ["es2022"],
    platform: "browser",
    write: false,
    legalComments: "none",
    plugins: [trimZodLocalesPlugin],
    define: {
      "process.env.NODE_ENV": '"production"',
    },
  }),
  readFile(STYLES_CSS, "utf8"),
]);

if (jsResult.outputFiles.length === 0) {
  throw new Error("esbuild produced no output");
}

const cssResult = await build({
  stdin: {
    contents: cssRaw,
    loader: "css",
    resolveDir: __dirname,
  },
  minify: true,
  write: false,
});

const bundledJs = escapeInlineScript(jsResult.outputFiles[0].text.trim());
const minifiedCss = escapeInlineStyle(cssResult.outputFiles[0].text.trim());

const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AGY Info</title>
<style>${minifiedCss}</style>
</head>
<body>
<div id="app"></div>
<script>${bundledJs}</script>
</body>
</html>
`;

const bytes = verifyBundle(html, minifiedCss, bundledJs);

await mkdir(dirname(OUT_HTML), { recursive: true });
await writeFile(OUT_HTML, html, "utf8");

console.log(
  `Built ${OUT_HTML} (${bytes} bytes / ${(bytes / 1024).toFixed(1)} KB; JS ${(bundledJs.length / 1024).toFixed(1)} KB, CSS ${(minifiedCss.length / 1024).toFixed(1)} KB)`
);
