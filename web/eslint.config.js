import tseslint from "typescript-eslint";
// 放在所有配置之后:只关掉与 prettier 冲突的格式规则,格式判断单一真源归 prettier
import eslintConfigPrettier from "eslint-config-prettier";

export default tseslint.config(
  { ignores: ["dist", "src/lib/api-types.d.ts"] },
  tseslint.configs.recommended,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
  eslintConfigPrettier,
);
