import type { HarnessClient, PermissionView } from '@harness/protocol';
import { PlainText } from './PlainText';

export function PermissionCard({ view, client }: { view: PermissionView; client: HarnessClient }) {
  const answer = (allow: boolean, allowAlways = false) => {
    void client.answerPermission({ requestId: view.requestId, allow, allowAlways }).catch(() => {
      /* resolution errors surface as stream notices */
    });
  };
  return (
    <div className="card permission">
      <div className="meta">permission · {view.kind}</div>
      <PlainText text={view.summary} />
      <div className="actions">
        <button onClick={() => answer(true)}>Allow</button>
        <button onClick={() => answer(true, true)}>Allow always</button>
        <button onClick={() => answer(false)}>Deny</button>
      </div>
    </div>
  );
}
