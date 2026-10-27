package net.odysseylabs.powerdeck

import android.app.Application
import android.content.Context
import net.odysseylabs.powerdeck.data.ConfigStore
import net.odysseylabs.powerdeck.data.KeystoreSecretBox
import net.odysseylabs.powerdeck.data.PowerDeck

class PowerDeckApp : Application() {
    val deck: PowerDeck by lazy { PowerDeck(this, ConfigStore(this, KeystoreSecretBox())) }
}

val Context.deck: PowerDeck get() = (applicationContext as PowerDeckApp).deck
