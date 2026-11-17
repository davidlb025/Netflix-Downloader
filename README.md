# Netflix Downloader

Aplicación de escritorio para Windows que permite preparar y administrar descargas de episodios y series disponibles en Netflix y organizar los archivos en una biblioteca local. Es un proyecto independiente, no oficial y sin afiliación con Netflix.

---

## Descargo de responsabilidad

> **Lee este aviso antes de utilizar la aplicación.**

Al instalar o utilizar el programa, ten en cuenta lo siguiente:

1. **Eres responsable del uso que hagas de la aplicación** y de los archivos que obtengas o generes.
2. **Utilízala únicamente cuando tengas autorización** y de conformidad con las leyes aplicables, las condiciones del servicio y los derechos de autor y de terceros. No la uses para piratear, compartir contenido sin permiso o eludir medidas de protección o restricciones de acceso.
3. Tener una cuenta, una suscripción o la posibilidad de reproducir un título **no concede por sí solo permiso para copiarlo o distribuirlo**.
4. El programa y los materiales que lo acompañan se proporcionan **«tal cual» y según disponibilidad**, sin garantías de funcionamiento ininterrumpido, compatibilidad, exactitud o idoneidad para un propósito concreto, en la medida permitida por la ley.
5. En la máxima medida permitida por la legislación aplicable, los autores y distribuidores no asumen responsabilidad por daños, pérdidas de datos, problemas de cuenta, reclamaciones de terceros u otras consecuencias derivadas de la instalación o el uso del programa. Esta limitación no excluye responsabilidades ni derechos que la ley no permita excluir o limitar.
6. Netflix y las demás marcas mencionadas pertenecen a sus respectivos titulares. Su mención no implica afiliación, patrocinio ni respaldo.

Este aviso es informativo y no constituye asesoramiento jurídico. Si no cuentas con autorización para el uso previsto, no utilices la aplicación.

---

## Uso rápido

1. Ejecuta `Netflix Downloader.exe`. Mantén junto al ejecutable los archivos y carpetas incluidos en la distribución.
2. Abre **Ajustes** e inicia sesión mediante el navegador integrado. Completa el acceso en la ventana que se abre.
3. En **Ajustes**, selecciona la carpeta donde quieres guardar los archivos.
4. Ve a **Preparar descarga**, pega el enlace de una serie o un episodio de Netflix y analiza el contenido.
5. Si has elegido una serie, selecciona la temporada y los episodios. Después, elige los idiomas de audio, los subtítulos y una de las calidades disponibles.
6. Añade la selección a la cola. En **Descargas** puedes consultar el progreso y cancelar una tarea.
7. Al terminar, abre **Biblioteca local** para acceder al archivo o a su carpeta, guardar subtítulos disponibles o administrar los elementos completados.

Si cierras la aplicación mientras hay tareas activas, estas se interrumpen y tendrás que volver a añadir el enlace para continuarlas.

## Requisitos

- Windows con Microsoft WebView2.
- Conexión a Internet y una cuenta de Netflix cuando el servicio la requiera.
- Archivos y carpetas de la distribución completos y en sus ubicaciones correspondientes.
- Los binarios `ffmpeg.exe` y `ffprobe.exe` deben encontrarse en `Netflix-workstation\assets`.

### Instalar FFmpeg

Los binarios no están incluidos en el repositorio porque GitHub no permite archivos superiores a 100 MB.

Desde la raíz del proyecto, ejecuta `DESCARGAR_FFMPEG.bat`. El programa descargará desde [la distribución oficial para Windows enlazada por FFmpeg](https://ffmpeg.org/download.html), extraerá `ffmpeg.exe` y `ffprobe.exe` y los colocará automáticamente en `Netflix-workstation\assets`.

Antes de iniciar la aplicación o crear la distribución, comprueba que existan ambos archivos:

```text
Netflix-workstation/assets/ffmpeg.exe
Netflix-workstation/assets/ffprobe.exe
```

## Archivos y disponibilidad

Los archivos se guardan en la carpeta de salida elegida. La organización de vídeos, pistas de audio y subtítulos depende de las opciones seleccionadas. Las temporadas, idiomas, subtítulos y calidades disponibles pueden variar según el título, la región, la cuenta y los cambios del servicio. No se garantiza la disponibilidad permanente ni la compatibilidad futura.

## Alternativas legítimas

Para ver contenido sin realizar copias, utiliza las funciones oficiales de Netflix. Para crear una biblioteca local, utiliza contenido propio, de dominio público o con una licencia que autorice expresamente la copia y el uso previsto.

## Otras herramientas del mismo creador

[Android WVD Lab](https://github.com/davidlb025/Android-WVD-Lab)

[Widevine CDM Grabber](https://github.com/davidlb025/Widevine-CDM-Grabber)
