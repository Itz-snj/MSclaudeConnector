import type { SessionState } from '@harness/protocol';
import { ItemView } from './ItemViews';

export function StreamView({ session, myDeviceId }: { session: SessionState; myDeviceId: string | null }) {
  if (session.items.length === 0) {
    return <div className="stream hint">No activity yet.</div>;
  }
  return (
    <div className="stream">
      {session.items.map((item) => (
        <ItemView key={item.id} item={item} session={session} myDeviceId={myDeviceId} />
      ))}
    </div>
  );
}
