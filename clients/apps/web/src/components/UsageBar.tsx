import type { UsageUpdateBody } from '@harness/protocol';

export function UsageBar({ usage }: { usage: UsageUpdateBody | null }) {
  if (!usage) return null;
  return (
    <span className="usage">
      in {usage.inputTokens} · out {usage.outputTokens}
    </span>
  );
}
