# 供应商配置导入导出与差异预览实现计划

## 目标

为 `/ai-providers` 页面增加按当前供应商标签页导入/导出 JSON 的能力，并在导入前展示规范化后的 diff，避免重复导入时产生重复、空值或顺序抖动导致的脏数据。

## 实施步骤

1. 在 `src/modules/providers/__tests__/` 为导出、导入前 diff 预览、重复导入去重写失败测试。
2. 新增 `src/modules/providers/provider-import-export.ts`，实现各供应商配置的规范化、去重、diff 汇总和 JSON 解析。
3. 在 `src/modules/providers/ProvidersPage.tsx` 增加当前 tab 的导入/导出操作、文件选择、diff 预览弹窗和确认导入流程。
4. 复用现有 `providersApi.get*` / `save*` 接口，对导入内容先规范化再整体覆盖保存，导出内容也统一走同一套序列化规则。
5. 更新 `src/i18n/locales/zh-CN.json`、`src/i18n/locales/en.json`、`src/i18n/locales/ru.json` 文案。
6. 运行定向测试、全量测试、lint、build 和 bundle diff，确认没有回归。

## 验证命令

```bash
bun run test src/modules/providers/__tests__/ProvidersPage.import-export.test.tsx
bun run test
bun run lint
bun run build
bun run bundle:diff
```
