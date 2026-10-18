import tseslint from "typescript-eslint";
// 放在所有配置之后:只关掉与 prettier 冲突的格式规则,格式判断单一真源归 prettier
import eslintConfigPrettier from "eslint-config-prettier";
// React hooks 规则单源(含 React Compiler 规则;独立 react-compiler 插件已并入本包,v7 flat 预设)
import eslintPluginReactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  { ignores: ["dist", "src/lib/api-types.d.ts"] },
  tseslint.configs.recommended,
  eslintPluginReactHooks.configs.flat.recommended,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
  eslintConfigPrettier,
);
