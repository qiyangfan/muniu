/** @type {import('stylelint').Config} */
export default {
  extends: ["stylelint-config-standard"],
  rules: {
    // Tailwind 4 的 @theme/@plugin/@custom-variant 等自定义 at-rule 不误报
    "at-rule-no-unknown": [
      true,
      {
        ignoreAtRules: [
          "theme",
          "plugin",
          "custom-variant",
          "apply",
          "reference",
          "utility",
          "variant",
          "source",
          "config",
        ],
      },
    ],
    "import-notation": null, // Tailwind 4 约定 @import "tailwindcss"
    // @apply/@theme 的 prelude 是 Tailwind 语义而非原生 at-rule 语法,标准校验必误报
    "at-rule-prelude-no-invalid": [
      true,
      { ignoreAtRules: ["apply", "theme", "utility", "variant"] },
    ],
  },
};
