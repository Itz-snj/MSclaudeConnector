import type { HarnessClient } from '@harness/protocol';

export function InterruptButton({ client, disabled }: { client: HarnessClient; disabled: boolean }) {
  return (
    <button
      disabled={disabled}
      onClick={() => {
        void client.interrupt().catch(() => {
          /* errors surface as stream notices */
        });
      }}
    >
      Interrupt
    </button>
  );
}
