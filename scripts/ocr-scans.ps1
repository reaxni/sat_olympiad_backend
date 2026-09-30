param([string]$InputDir,[string]$OutputPath)
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null=[Windows.Storage.StorageFile,Windows.Storage,ContentType=WindowsRuntime]
$null=[Windows.Graphics.Imaging.BitmapDecoder,Windows.Graphics.Imaging,ContentType=WindowsRuntime]
$null=[Windows.Media.Ocr.OcrEngine,Windows.Foundation,ContentType=WindowsRuntime]
$null=[Windows.Globalization.Language,Windows.Globalization,ContentType=WindowsRuntime]
$asTask=([System.WindowsRuntimeSystemExtensions].GetMethods()|Where-Object {$_.Name -eq 'AsTask' -and $_.IsGenericMethod -and $_.GetParameters().Count -eq 1})[0]
function Await($operation,$type){$task=$asTask.MakeGenericMethod($type).Invoke($null,@($operation)); $task.Wait(); $task.Result}
$engine=[Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage([Windows.Globalization.Language]::new('en-US'))
$results=@()
Get-ChildItem -LiteralPath $InputDir -Filter 'ocr-*.png'|Sort-Object Name|ForEach-Object {
 $file=Await ([Windows.Storage.StorageFile]::GetFileFromPathAsync($_.FullName)) ([Windows.Storage.StorageFile])
 $stream=Await ($file.OpenAsync([Windows.Storage.FileAccessMode]::Read)) ([Windows.Storage.Streams.IRandomAccessStream])
 $decoder=Await ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
 $bitmap=Await ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
 $result=Await ($engine.RecognizeAsync($bitmap)) ([Windows.Media.Ocr.OcrResult])
 $lines=@($result.Lines|ForEach-Object {$line=$_; [pscustomobject]@{text=$line.Text; words=@($line.Words|ForEach-Object {[pscustomobject]@{text=$_.Text;x=$_.BoundingRect.X;y=$_.BoundingRect.Y;w=$_.BoundingRect.Width;h=$_.BoundingRect.Height}})}})
 $results+= [pscustomobject]@{file=$_.Name;text=$result.Text;lines=$lines}
 $bitmap.Dispose();$stream.Dispose()
}
$results|ConvertTo-Json -Depth 8|Set-Content -LiteralPath $OutputPath -Encoding UTF8
