' TVBox 4.4 LAN API - silent launcher
' Puts start-server.bat in the project folder and runs it with no console window.
' A shortcut/copy of this file lives in the Startup folder so the API is up after logon.

Dim sh, fso, here, bat
Set sh = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")

here = fso.GetParentFolderName(WScript.ScriptFullName)
bat = here & "\start-server.bat"

If Not fso.FileExists(bat) Then
  ' Fallback: the file was copied into Startup, point at the real project path.
  here = "C:\Backup\Documents\deepseek-harness\default-workspace\tvbox44-crawler"
  bat = here & "\start-server.bat"
End If

sh.CurrentDirectory = here
sh.Run """" & bat & """", 0, False
