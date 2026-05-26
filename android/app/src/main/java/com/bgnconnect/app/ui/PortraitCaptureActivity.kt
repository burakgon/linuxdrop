package com.bgnconnect.app.ui

import com.journeyapps.barcodescanner.CaptureActivity

/** QR scanner locked to portrait (manifest screenOrientation), so it doesn't flip to landscape. */
class PortraitCaptureActivity : CaptureActivity()
