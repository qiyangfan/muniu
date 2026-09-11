import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "src/lib/api-types.d.ts"] },
  tseslint.configs.recommended,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  }
);
