# MyRouter — Aturan Kerja Repo

## Bersih-bersih setiap ada build baru (WAJIB)

Setiap kali build/rilis baru selesai (`dist\MyRouter.exe` atau `myrouter.exe`), rapikan folder repo pada sesi yang sama:

1. Jalankan `.\scripts\clean-artifacts.ps1 -DryRun` untuk melihat kandidat, lalu jalankan tanpa `-DryRun`.
2. Yang boleh dibersihkan hanya artefak: binary lama di `dist\`, folder `.backup-patch-*` lama, isi `qa-artifacts` lama, `data-shots`, dan backup DB lama.
3. Yang TIDAK boleh dihapus: `dist\MyRouter.exe` terbaru, `dist\data\`, `data\`, `web\node_modules`, source code, `docs/`, `assets/`, `stitch_myrouter_ai_gateway_ui_kit\`.
4. Data runtime dan kunci (`master.key`, `*.db` di folder aktif) tidak boleh dihapus tanpa izin eksplisit.
5. Setelah bersih-bersih, laporkan singkat: apa yang dihapus dan berapa ruang yang dibebaskan.

## Desktop selalu pakai versi terbaru (WAJIB)

Setelah setiap build desktop, pastikan `MyRouter.lnk` di Desktop menunjuk ke binary terbaru:

1. Jalankan `.\scripts\install-desktop.ps1` (default `dist\MyRouter.exe`).
2. Shortcut dibuat dengan `-install-only` (tanpa menjalankan server) dan mengarah ke path build, jadi rebuild berikutnya otomatis terpakai.
3. Build desktop sudah melakukan ini otomatis; jangan lewati kecuali dengan `-NoDesktopShortcut`.

## Versi selalu naik setiap build (WAJIB)

1. Nomor versi disimpan di `VERSION` (format `MAJOR.MINOR.PATCH`, tanpa `v`).
2. `build-windows.ps1` otomatis menaikkan patch (`0.1.0` → `0.1.1` → ...) lalu menyuntikkannya ke binary via `-ldflags "-X main.version=..."`.
3. `VERSION` baru ditulis setelah build sukses, jadi build gagal tidak menghabiskan nomor.
4. Jangan commit build tanpa memperbarui `VERSION`; naikkan manual dengan `-Version 1.2.3` bila perlu lompat versi, atau `-NoVersionBump` untuk rebuild versi yang sama.
5. Versi yang dilihat user berasal dari server (`/healthz`, halaman Settings), bukan hardcode di frontend.

## Build & verifikasi

- Desktop (tray, tanpa console): `.\scripts\build-windows.ps1 -Gui` → `dist\MyRouter.exe` + shortcut Desktop + cleanup otomatis
- Console/server: `.\scripts\build-windows.ps1` → `myrouter.exe` + cleanup otomatis
- Verifikasi: `go test ./...` dan `npm --prefix web run build`
