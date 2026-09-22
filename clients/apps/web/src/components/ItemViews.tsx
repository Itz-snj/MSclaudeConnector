import type { Item, SessionState } from '@harness/protocol';
import { displayName } from '@harness/protocol';
import { PlainText } from './PlainText';

export function ItemView({ item, session, myDeviceId }: { item: Item; session: SessionState; myDeviceId: string | null }) {
  switch (item.kind) {
    case 'prompt':
      return (
        <div className="item prompt">
          <div className="meta">{displayName(session, item.byDevice, myDeviceId ?? undefined)}</div>
          <PlainText text={item.text} />
        </div>
      );
    case 'assistant':
      return (
        <div className="item assistant">
          <div className="meta">assistant</div>
          <PlainText text={item.text} />
        </div>
      );
    case 'tool':
      return <ToolView item={item} />;
    case 'permission':
      return <PermissionView item={item} />;
    case 'question':
      return <QuestionView item={item} />;
    case 'notice':
      return (
        <div className={`item notice ${item.level}`}>
          <PlainText text={item.text} />
        </div>
      );
    default:
      return null;
  }
}

function ToolView({ item }: { item: Extract<Item, { kind: 'tool' }> }) {
  return (
    <details className="item tool" open={!item.done}>
      <summary>
        {item.name} {item.done ? '' : '…'}
      </summary>
      {item.input ? (
        <>
          <div className="meta">input</div>
          <PlainText text={item.input} />
        </>
      ) : null}
      {item.output ? (
        <>
          <div className="meta">output</div>
          <PlainText text={item.output} />
        </>
      ) : null}
    </details>
  );
}

function PermissionView({ item }: { item: Extract<Item, { kind: 'permission' }> }) {
  const resolved = item.resolved;
  return (
    <div className={`item permission ${resolved ? 'resolved' : ''}`}>
      <div className="meta">permission · {item.permissionKind}</div>
      <PlainText text={item.summary} />
      {resolved ? (
        <div className="hint">
          {resolved.allow ? 'Allowed' : 'Denied'} by {resolved.byName || resolved.byDevice}
        </div>
      ) : null}
    </div>
  );
}

function QuestionView({ item }: { item: Extract<Item, { kind: 'question' }> }) {
  const resolved = item.resolved;
  return (
    <div className={`item question ${resolved ? 'resolved' : ''}`}>
      <div className="meta">question</div>
      <PlainText text={item.text} />
      {resolved ? (
        <div className="hint">
          Answered {resolved.text} — by {resolved.byName || resolved.byDevice}
        </div>
      ) : null}
    </div>
  );
}
