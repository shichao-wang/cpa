import type { Connection } from "./api";

export function CompatibilityNotice({
  connection,
  model,
}: {
  connection: Connection;
  model?: string;
}) {
  if (connection.protocol !== "responses") return null;
  const modelID = model || connection.models[0]?.id;
  const capability = modelID ? connection.compatibility?.[modelID] : undefined;
  return (
    <details className="compatibility-notice">
      <summary>
        Responses：输出上限不执行，上下文容量未验证
        {model && capability && (
          <>
            {" "}
            ·{" "}
            {capability.reasoning === "effort-mapping"
              ? "推理按 effort 映射"
              : "推理能力未知"}
          </>
        )}
      </summary>
      <ul>
        {capability ? (
          capability.warnings
            .slice(0, model ? undefined : 3)
            .map((warning) => <li key={warning}>{warning}</li>)
        ) : (
          <li>请刷新页面加载该模型的能力说明；不要将调用成功视为参数生效。</li>
        )}
      </ul>
    </details>
  );
}
