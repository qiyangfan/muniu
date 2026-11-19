import tseslint from "typescript-eslint";
// 放在所有配置之后:只关掉与 prettier 冲突的格式规则,格式判断单一真源归 prettier
import eslintConfigPrettier from "eslint-config-prettier";
// React hooks 规则单源(含 React Compiler 规则;独立 react-compiler 插件已并入本包,v7 flat 预设)
import eslintPluginReactHooks from "eslint-plugin-react-hooks";
// 无障碍规则的静态执法(前端宪法 Article 9);插件 peer 声明尚未覆盖 eslint 10,
// flat 预设实际可加载,升级插件后应移除本句注释
import jsxA11y from "eslint-plugin-jsx-a11y";

export default tseslint.config(
  { ignores: ["dist", "src/lib/api-types.d.ts"] },
  tseslint.configs.recommended,
  eslintPluginReactHooks.configs.flat.recommended,
  jsxA11y.flatConfigs.recommended,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
  eslintConfigPrettier,
);
