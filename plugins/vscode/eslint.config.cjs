const js = require("@eslint/js");
const tsParser = require("@typescript-eslint/parser");
const tsPlugin = require("@typescript-eslint/eslint-plugin");

module.exports = [
  {
    ignores: ["out/**", "node_modules/**"],
  },
  js.configs.recommended,
  {
    files: ["src/**/*.ts"],
    languageOptions: {
      parser: tsParser,
      ecmaVersion: "latest",
      sourceType: "module",
      globals: {
        fetch: "readonly",
        console: "readonly",
        AbortSignal: "readonly",
        DOMException: "readonly",
        NodeJS: "readonly",
        setTimeout: "readonly",
        clearTimeout: "readonly",
        setInterval: "readonly",
        clearInterval: "readonly",
        Response: "readonly",
        AbortController: "readonly",
        process: "readonly",
        TextDecoder: "readonly",
        TextEncoder: "readonly",
        ReadableStreamDefaultReader: "readonly",
      },
    },
    plugins: {
      "@typescript-eslint": tsPlugin,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,
      "@typescript-eslint/no-explicit-any": "off",
    },
  },
  {
    // Test files can live under any __tests__ directory, not just
    // src/__tests__/. Use a glob that matches all of them.
    files: ["src/**/__tests__/**/*.ts"],
    languageOptions: {
      globals: {
        jest: "readonly",
        describe: "readonly",
        test: "readonly",
        it: "readonly",
        expect: "readonly",
        beforeEach: "readonly",
        afterEach: "readonly",
        global: "readonly",
        Response: "readonly",
        RequestInit: "readonly",
        ResponseInit: "readonly",
        TextEncoder: "readonly",
        TextDecoder: "readonly",
        ReadableStreamDefaultReader: "readonly",
      },
    },
  },
];
