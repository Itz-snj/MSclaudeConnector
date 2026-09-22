import { StyleSheet, Text } from 'react-native';

/**
 * The only component that renders agent-controlled text. It is a plain Text
 * child with data detection disabled, so no link is auto-created and no HTML
 * is interpreted. Never enable android:autoLink.
 */
export function PlainText({ text }: { text: string }) {
  return (
    <Text selectable dataDetectorType="none" style={styles.text}>
      {text}
    </Text>
  );
}

const styles = StyleSheet.create({
  text: { color: '#e2e8f0', fontSize: 15, lineHeight: 21 },
});
