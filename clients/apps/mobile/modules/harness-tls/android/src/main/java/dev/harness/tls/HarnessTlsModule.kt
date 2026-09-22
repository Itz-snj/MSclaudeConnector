package dev.harness.tls

import com.facebook.react.modules.network.OkHttpClientProvider
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import okhttp3.OkHttpClient
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager

private data class SpkiPin(val host: String, val port: Int, val spkiHex: String)

/**
 * Android self-signed TLS support.
 *
 * RN's WebSocket transport is OkHttp, so a JS-only trust override is
 * impossible. This module replaces OkHttp's client factory with one whose
 * trust manager accepts a chain when the leaf certificate's SubjectPublicKeyInfo
 * matches a QR-pinned value, and delegates to the platform default otherwise.
 * The hostname verifier likewise accepts a matching pin (the self-signed cert
 * has no DNS/IP SAN the platform will trust) and delegates for everything else.
 *
 * Pinning the SPKI rather than the certificate keeps the pin stable when the
 * host regenerates its certificate with the same key.
 */
class HarnessTlsModule : Module() {
  @Volatile
  private var pins: List<SpkiPin> = emptyList()

  @Volatile
  private var installed = false

  override fun definition() = ModuleDefinition {
    Name("HarnessTls")

    Function("setPinnedHosts") { hosts: List<Map<String, Any?>> ->
      pins = hosts.mapNotNull { map ->
        val host = (map["host"] as? String)?.lowercase() ?: return@mapNotNull null
        val port = (map["port"] as? Number)?.toInt() ?: 443
        val spki = (map["spki"] as? String)?.lowercase() ?: return@mapNotNull null
        SpkiPin(host, port, spki)
      }
      install()
      null
    }
  }

  private fun install() {
    if (installed) return
    installed = true

    val delegate = platformTrustManager()
    val pinningTrustManager = PinningTrustManager(delegate) { pins }
    val sslContext = SSLContext.getInstance("TLS")
    sslContext.init(null, arrayOf(pinningTrustManager), null)

    val defaultHostnameVerifier = HttpsURLConnection.getDefaultHostnameVerifier()
    val hostnameVerifier = HostnameVerifier { hostname, session ->
      val leaf = session.peerCertificates.firstOrNull() as? X509Certificate
      val current = pins
      if (leaf != null &&
        current.any { it.host.equals(hostname, ignoreCase = true) && constantTimeEquals(spkiHex(leaf), it.spkiHex) }
      ) {
        true
      } else {
        defaultHostnameVerifier.verify(hostname, session)
      }
    }

    OkHttpClientProvider.setOkHttpClientFactory {
      OkHttpClient.Builder()
        .sslSocketFactory(sslContext.socketFactory, pinningTrustManager)
        .hostnameVerifier(hostnameVerifier)
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)
        .build()
    }
  }
}

private class PinningTrustManager(
  private val delegate: X509TrustManager,
  private val pinsProvider: () -> List<SpkiPin>,
) : X509TrustManager {
  override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
    delegate.checkClientTrusted(chain, authType)

  override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
    val leaf = chain?.firstOrNull()
    val pins = pinsProvider()
    if (leaf != null && pins.any { constantTimeEquals(spkiHex(leaf), it.spkiHex) }) {
      return
    }
    delegate.checkServerTrusted(chain, authType)
  }

  override fun getAcceptedIssuers(): Array<X509Certificate> = delegate.acceptedIssuers
}

private fun platformTrustManager(): X509TrustManager {
  val factory = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm())
  factory.init(null as KeyStore?)
  return factory.trustManagers.filterIsInstance<X509TrustManager>().first()
}

private fun spkiHex(cert: X509Certificate): String {
  val digest = MessageDigest.getInstance("SHA-256").digest(cert.publicKey.encoded)
  return digest.joinToString("") { byte -> "%02x".format(byte) }
}

private fun constantTimeEquals(a: String, b: String): Boolean {
  if (a.length != b.length) return false
  var result = 0
  for (i in a.indices) {
    result = result or (a[i].code xor b[i].code)
  }
  return result == 0
}
