import { ScrollView, StyleSheet, Text, View } from 'react-native';
import { displayName, type Item, type SessionState } from '@harness/protocol';
import { PlainText } from './PlainText';

export function StreamView({
  session,
  myDeviceId,
}: {
  session: SessionState;
  myDeviceId: string | null;
}) {
  return (
    <ScrollView style={styles.stream} contentContainerStyle={styles.content}>
      {session.items.map((item) => (
        <ItemView key={item.id} item={item} session={session} myDeviceId={myDeviceId} />
      ))}
    </ScrollView>
  );
}

function ItemView({
  item,
  session,
  myDeviceId,
}: {
  item: Item;
  session: SessionState;
  myDeviceId: string | null;
}) {
  switch (item.kind) {
    case 'prompt':
      return (
        <View style={[styles.item, styles.prompt]}>
          <Text style={styles.meta}>{displayName(session, item.byDevice, myDeviceId ?? undefined)}</Text>
          <PlainText text={item.text} />
        </View>
      );
    case 'assistant':
      return (
        <View style={styles.item}>
          <Text style={styles.meta}>assistant</Text>
          <PlainText text={item.text} />
        </View>
      );
    case 'tool':
      return (
        <View style={styles.item}>
          <Text style={styles.meta}>
            tool · {item.name} {item.done ? '' : '…'}
          </Text>
          {item.input ? <PlainText text={item.input} /> : null}
          {item.output ? <PlainText text={item.output} /> : null}
        </View>
      );
    case 'permission':
      return (
        <View style={[styles.item, item.resolved ? styles.resolved : null]}>
          <Text style={styles.meta}>permission · {item.permissionKind}</Text>
          <PlainText text={item.summary} />
          {item.resolved ? (
            <Text style={styles.hint}>
              {item.resolved.allow ? 'Allowed' : 'Denied'} by{' '}
              {item.resolved.byName || item.resolved.byDevice}
            </Text>
          ) : null}
        </View>
      );
    case 'question':
      return (
        <View style={[styles.item, item.resolved ? styles.resolved : null]}>
          <Text style={styles.meta}>question</Text>
          <PlainText text={item.text} />
          {item.resolved ? (
            <Text style={styles.hint}>
              Answered {item.resolved.text} — by {item.resolved.byName || item.resolved.byDevice}
            </Text>
          ) : null}
        </View>
      );
    case 'notice':
      return (
        <View style={styles.item}>
          <PlainText text={item.text} />
        </View>
      );
    default:
      return null;
  }
}

const styles = StyleSheet.create({
  stream: { flex: 1 },
  content: { gap: 8, paddingVertical: 8 },
  item: {
    backgroundColor: '#131c2e',
    borderWidth: 1,
    borderColor: '#26344f',
    borderRadius: 8,
    padding: 10,
    gap: 4,
  },
  prompt: { borderLeftWidth: 3, borderLeftColor: '#38bdf8' },
  resolved: { opacity: 0.55 },
  meta: { color: '#94a3b8', fontSize: 11, textTransform: 'uppercase' },
  hint: { color: '#94a3b8', fontSize: 12 },
});
