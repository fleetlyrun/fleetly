# Normalize every .bat under spike\c\scripts to CRLF + ASCII so cmd.exe is
# happy (labels/goto included). .sh files must stay LF - only *.bat touched.
$files = Get-ChildItem -Path "D:\Codes\qiulin\edgesets\spike\c\scripts" -Filter *.bat
foreach ($f in $files) {
  $t = [IO.File]::ReadAllText($f.FullName)
  $t = $t -replace "`r`n", "`n"
  $t = $t -replace "`n", "`r`n"
  [IO.File]::WriteAllText($f.FullName, $t, [Text.Encoding]::ASCII)
  Write-Output ("CRLF: " + $f.Name)
}
