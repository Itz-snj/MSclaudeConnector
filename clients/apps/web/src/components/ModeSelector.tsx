import type { HarnessClient } from '@harness/protocol';

const MODES = ['default', 'acceptEdits', 'bypassPermissions', 'plan'];

export function ModeSelector({
  client,
  mode,
  disabled,
}: {
  client: HarnessClient;
  mode: string;
  disabled: boolean;
}) {
  return (
    <select
      value={mode || 'default'}
      disabled={disabled}
      onChange={(e) => {
        void client.setMode(e.target.value).catch(() => {
          /* errors surface as stream notices */
        });
      }}
      aria-label="Mode"
    >
      {MODES.map((m) => (
        <option key={m} value={m}>
          {m}
        </option>
      ))}
    </select>
  );
}
