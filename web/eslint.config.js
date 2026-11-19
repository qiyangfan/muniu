import tseslint from "typescript-eslint";
// 放在所有配置之后:只关掉与 prettier 冲突的格式规则,格式判断单一真源归 prettier
import eslintConfigPrettier from "eslint-config-prettier";
// React hooks 规则单源(含 React Compiler 规则;独立 react-compiler 插件已并入本包,v7 flat 预设)
import eslintPluginReactHooks from "eslint-plugin-react-hooks";
// 无障碍规则的静态执法(前端宪法 Article 9);插件 peer 声明尚未覆盖 eslint 10,
// flat 预设实际可加载,升级插件后应移除本句注释
import jsxA11y from "eslint-plugin-jsx-a11y";

export default tseslint.config(
  // 配置脚本与 Node 工具不在 TS 主工程内：eslint/stylelint 配置是纯 JS，coverage-gate 是 Node 脚本。
  {
    ignores: [
      "dist",
      "src/lib/api-types.d.ts",
      "eslint.config.js",
      "stylelint.config.js",
      "scripts/**/*.mjs",
    ],
  },
  // 类型感知规则集:no-floating-promises / no-misused-promises / no-unsafe-* 只在有类型信息时成立。
  // 其余项目文件(配置脚本)不在 tsconfig 主工程内,由 allowDefaultProject 以独立程序承载。
  tseslint.configs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
  eslintPluginReactHooks.configs.flat.recommended,
  jsxA11y.flatConfigs.recommended,
  eslintConfigPrettier,
);
