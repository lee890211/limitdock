Set shell = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
root = fso.GetParentFolderName(WScript.ScriptFullName)
shell.CurrentDirectory = root
exe = root & "\LimitDock.exe"
script = root & "\LimitDock.ps1"
If fso.FileExists(exe) Then
  cmd = Chr(34) & exe & Chr(34)
Else
  cmd = "powershell.exe -STA -NoProfile -ExecutionPolicy Bypass -File " & Chr(34) & script & Chr(34)
End If
shell.Run cmd, 0, False
