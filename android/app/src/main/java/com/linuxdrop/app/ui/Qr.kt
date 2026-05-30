package com.linuxdrop.app.ui

import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import com.google.zxing.BarcodeFormat
import com.journeyapps.barcodescanner.BarcodeEncoder

/** Encodes [content] as a QR code bitmap for display. */
fun encodeQr(content: String, size: Int = 720): ImageBitmap =
    BarcodeEncoder().encodeBitmap(content, BarcodeFormat.QR_CODE, size, size).asImageBitmap()
