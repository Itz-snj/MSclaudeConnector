import { useEffect, useState } from 'react';
import { ActivityIndicator, StyleSheet, View } from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider } from 'react-native-safe-area-context';
import { bootstrap, getMobileClient, useMobileState } from './mobileClient';
import { PairScreen } from './screens/PairScreen';
import { SessionScreen } from './screens/SessionScreen';

export default function App() {
  const [ready, setReady] = useState(false);

  useEffect(() => {
    void bootstrap().then(() => setReady(true));
  }, []);

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      {ready ? (
        <Root />
      ) : (
        <View style={styles.loading}>
          <ActivityIndicator color="#38bdf8" />
        </View>
      )}
    </SafeAreaProvider>
  );
}

function Root() {
  const state = useMobileState();
  const client = getMobileClient();
  const needsPairing = state.myDeviceId === null || state.phase === 'unauthorized';
  return needsPairing ? (
    <PairScreen state={state} />
  ) : (
    <SessionScreen state={state} client={client} />
  );
}

const styles = StyleSheet.create({
  loading: { flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: '#0b1220' },
});
