import { useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from 'expo-camera';
import { pairingCode, parsePairingPayload, type ClientState } from '@harness/protocol';
import { forgetDevice, pairWithInfo } from '../mobileClient';

export function PairScreen({ state }: { state: ClientState }) {
  const [permission, requestPermission] = useCameraPermissions();
  const [scanned, setScanned] = useState(false);
  const [manual, setManual] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (permission && !permission.granted) void requestPermission();
  }, [permission, requestPermission]);

  const handle = async (raw: string) => {
    if (!raw.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await pairWithInfo(parsePairingPayload(raw));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setScanned(false);
    } finally {
      setBusy(false);
    }
  };

  const onScan = (result: BarcodeScanningResult) => {
    if (scanned || busy) return;
    setScanned(true);
    void handle(result.data);
  };

  const manualCode = manual.trim() && !manual.trim().startsWith('{') ? pairingCode(manual.trim()) : '';

  return (
    <View style={styles.container}>
      <Text style={styles.title}>Pair with host</Text>
      {state.phase === 'unauthorized' ? (
        <Text style={styles.error}>
          This device is no longer authorized. Scan the host QR again to re-pair.
        </Text>
      ) : (
        <Text style={styles.hint}>Scan the host&apos;s pairing QR, then confirm the 4-character code.</Text>
      )}

      <View style={styles.camera}>
        {permission?.granted ? (
          <CameraView
            style={StyleSheet.absoluteFill}
            barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
            onBarcodeScanned={scanned || busy ? undefined : onScan}
          />
        ) : (
          <Text style={styles.hint}>Camera permission is required to scan the pairing QR.</Text>
        )}
      </View>

      <Text style={styles.hint}>Or paste the pairing JSON / token:</Text>
      <TextInput
        style={styles.input}
        value={manual}
        onChangeText={setManual}
        placeholder="token or JSON"
        placeholderTextColor="#64748b"
        autoCapitalize="none"
        autoCorrect={false}
      />
      {manualCode ? <Text style={styles.code}>{manualCode}</Text> : null}

      <Pressable style={styles.button} onPress={() => void handle(manual)} disabled={busy || !manual}>
        <Text style={styles.buttonText}>{busy ? 'Pairing…' : 'Pair'}</Text>
      </Pressable>

      {error ? <Text style={styles.error}>{error}</Text> : null}

      {state.myDeviceId ? (
        <Pressable onPress={() => void forgetDevice()}>
          <Text style={styles.link}>Forget this device</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0b1220', padding: 16, gap: 12 },
  title: { color: '#38bdf8', fontSize: 22, fontWeight: '700' },
  hint: { color: '#94a3b8', fontSize: 14 },
  error: { color: '#f87171', fontSize: 14 },
  camera: {
    height: 280,
    borderRadius: 12,
    overflow: 'hidden',
    borderWidth: 1,
    borderColor: '#26344f',
    alignItems: 'center',
    justifyContent: 'center',
  },
  input: {
    borderWidth: 1,
    borderColor: '#26344f',
    borderRadius: 8,
    color: '#e2e8f0',
    paddingHorizontal: 10,
    paddingVertical: 10,
    backgroundColor: '#1b2740',
  },
  code: { color: '#38bdf8', fontSize: 28, letterSpacing: 8, fontWeight: '700' },
  button: { backgroundColor: '#0369a1', borderRadius: 8, paddingVertical: 12, alignItems: 'center' },
  buttonText: { color: '#f8fafc', fontWeight: '700' },
  link: { color: '#38bdf8', textAlign: 'center', paddingVertical: 8 },
});
